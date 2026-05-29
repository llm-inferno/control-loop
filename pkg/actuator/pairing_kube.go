package actuator

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// isPodReady returns true iff status.conditions has type=Ready, status=True.
// Note: this is the K8s Ready condition; it is distinct from controller.IsPodReady,
// which is a startup-delay check used by the Collector.
func isPodReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// listOwnedReadyPods returns Pods owned (transitively via ReplicaSet) by the given
// Deployment whose Ready condition is True. Snapshots are returned in the order
// returned by the K8s API (callers should sort if determinism matters).
func listOwnedReadyPods(ctx context.Context, kc kubernetes.Interface, dep *appsv1.Deployment, pairLabelKey string) ([]PodSnapshot, error) {
	selectorStr := labels.Set(dep.Spec.Selector.MatchLabels).String()
	// Find ReplicaSets owned by the Deployment.
	rsList, err := kc.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, fmt.Errorf("list replicasets in %s: %w", dep.Namespace, err)
	}
	rsUIDs := map[types.UID]struct{}{}
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == dep.UID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}
	if len(rsUIDs) == 0 {
		return nil, nil
	}
	// List pods in the namespace and filter to owned + Ready.
	pods, err := kc.CoreV1().Pods(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", dep.Namespace, err)
	}
	out := make([]PodSnapshot, 0, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		owned := false
		for _, owner := range p.OwnerReferences {
			if _, ok := rsUIDs[owner.UID]; ok {
				owned = true
				break
			}
		}
		if !owned || !isPodReady(p) {
			continue
		}
		out = append(out, PodSnapshot{
			Name:      p.Name,
			Namespace: p.Namespace,
			Ready:     true,
			PairID:    p.Labels[pairLabelKey],
		})
	}
	return out, nil
}

// jsonPatchEscape escapes a JSON-pointer segment per RFC6901 (~ -> ~0, / -> ~1).
func jsonPatchEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

// setPodLabel writes a label on a pod via JSON patch (op=add is idempotent for replace).
func setPodLabel(ctx context.Context, kc kubernetes.Interface, ns, name, key, value string) error {
	patch := []byte(fmt.Sprintf(`[{"op":"add","path":"/metadata/labels/%s","value":%q}]`,
		jsonPatchEscape(key), value))
	_, err := kc.CoreV1().Pods(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

// removePodLabel clears a label on a pod via JSON patch op=remove. Idempotent: a 422
// (label absent) is treated as success.
func removePodLabel(ctx context.Context, kc kubernetes.Interface, ns, name, key string) error {
	patch := []byte(fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/%s"}]`, jsonPatchEscape(key)))
	_, err := kc.CoreV1().Pods(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if k8serrors.IsInvalid(err) {
		// Real K8s server returns 422 Invalid when JSON-patch op=remove targets
		// an absent label; treat this as success since our intent (label gone) is
		// satisfied. NOTE: client-go fake clients return a raw evanphx/json-patch
		// error instead of a typed *StatusError, so this idempotency path is only
		// covered against a real API server.
		return nil
	}
	return err
}

// setDeploymentReplicas patches spec.replicas to n.
func setDeploymentReplicas(ctx context.Context, kc kubernetes.Interface, ns, name string, n int32) error {
	patch := []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/replicas","value":%d}]`, n))
	_, err := kc.AppsV1().Deployments(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}
