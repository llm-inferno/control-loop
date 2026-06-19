package actuator

import (
	"context"
	"errors"
	"fmt"
	"strconv"

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
	var patchErrs []error
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
		if e := setPodLabel(ctx, kc, ns, p.Name, ctrl.KeyAccelerator, accelerator); e != nil {
			patchErrs = append(patchErrs, fmt.Errorf("pod %s accelerator: %w", p.Name, e))
		}
		if e := setPodLabel(ctx, kc, ns, p.Name, ctrl.KeyMaxBatchSize, strconv.Itoa(maxBatch)); e != nil {
			patchErrs = append(patchErrs, fmt.Errorf("pod %s maxbatchsize: %w", p.Name, e))
		}
	}
	return errors.Join(patchErrs...)
}
