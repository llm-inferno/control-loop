package actuator

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// Find ReplicaSets owned by the Deployment.
	rsList, err := kc.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{})
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
	pods, err := kc.CoreV1().Pods(dep.Namespace).List(ctx, metav1.ListOptions{})
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
