package actuator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// patchPodsAllocation writes the current allocation (accelerator + maxbatchsize)
// onto each running pod of the deployment, so the server-sim generator can read
// the in-force M* from its downward-API labels volume. Best-effort per pod: a
// transient patch error does not abort the remaining pods, but every failure is
// surfaced in the returned (joined) error rather than swallowed. A silently
// dropped maxbatchsize patch leaves the pod's label stale, so the Collector's
// coherence check excludes it every cycle with no diagnostic — so callers must
// log the returned error.
//
// Pods are filtered to those owned by the deployment's current ReplicaSets (same
// discipline as the Collector), so a draining old-rollout pod or another
// deployment that happens to share the label selector is not relabelled.
//
// Both labels are written in a single JSON patch, and a pod already carrying the
// target accelerator+maxbatchsize is skipped — so steady state (allocation
// unchanged) issues zero PATCHes and does not churn resourceVersion or force a
// downward-API volume re-projection.
//
// The per-pod PATCHes are independent round-trips, so they are issued
// concurrently (same fan-out discipline as the Collector's per-pod GET /latest):
// actuate latency tracks the slowest single PATCH rather than the sum over pods.
// The client's own QPS/burst limiter throttles the fan-out, so it is left
// unbounded to match the Collector.
func patchPodsAllocation(ctx context.Context, kc kubernetes.Interface, ns, depName, accelerator string, maxBatch int) error {
	dep, err := kc.AppsV1().Deployments(ns).Get(ctx, depName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	selector := labels.Set(dep.Spec.Selector.MatchLabels).String()

	rsList, err := kc.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	rsUIDs := make(map[types.UID]struct{})
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == dep.UID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}

	pods, err := kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	maxBatchStr := strconv.Itoa(maxBatch)

	// First pass (sequential): select the pods that actually need patching —
	// running, owned by this deployment's ReplicaSets, and not already carrying
	// the target allocation. The skip-if-unchanged guard keeps steady state at
	// zero PATCHes.
	var toPatch []string
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		owned := false
		for _, owner := range p.OwnerReferences {
			if _, ok := rsUIDs[owner.UID]; ok {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if p.Labels[ctrl.KeyAccelerator] == accelerator && p.Labels[ctrl.KeyMaxBatchSize] == maxBatchStr {
			continue // allocation already in force on this pod; skip to avoid churn
		}
		toPatch = append(toPatch, p.Name)
	}

	// Second pass (fan-out): patch the selected pods concurrently. Best-effort
	// per pod — a transient error does not abort the others; each failure is
	// captured by index and surfaced in the joined error.
	patchErrs := make([]error, len(toPatch))
	var wg sync.WaitGroup
	for i, name := range toPatch {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			if e := patchPodAllocationLabels(ctx, kc, ns, name, accelerator, maxBatchStr); e != nil {
				patchErrs[i] = fmt.Errorf("pod %s allocation: %w", name, e)
			}
		}(i, name)
	}
	wg.Wait()
	return errors.Join(patchErrs...)
}

// patchPodAllocationLabels writes the accelerator and maxbatchsize labels in a
// single JSON patch (op=add is idempotent for replace), so one PATCH triggers a
// single resourceVersion bump and downward-API re-projection per pod.
func patchPodAllocationLabels(ctx context.Context, kc kubernetes.Interface, ns, name, accelerator, maxBatch string) error {
	patch := []byte(fmt.Sprintf(
		`[{"op":"add","path":"/metadata/labels/%s","value":%q},{"op":"add","path":"/metadata/labels/%s","value":%q}]`,
		jsonPatchEscape(ctrl.KeyAccelerator), accelerator,
		jsonPatchEscape(ctrl.KeyMaxBatchSize), maxBatch))
	_, err := kc.CoreV1().Pods(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}
