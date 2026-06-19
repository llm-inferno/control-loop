package actuator

import (
	"context"
	"testing"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPatchPodsAllocation(t *testing.T) {
	// dep -> rs -> p1 is the real ownership chain; p2 shares the label selector
	// but is not owned by dep's ReplicaSet (e.g. a foreign deployment), so it
	// must be left untouched.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dep", Namespace: "ns", UID: "dep-uid"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dep-rs", Namespace: "ns", UID: "rs-uid",
			Labels:          map[string]string{"app": "x"},
			OwnerReferences: []metav1.OwnerReference{{UID: "dep-uid"}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p1", Namespace: "ns", Labels: map[string]string{"app": "x"},
			OwnerReferences: []metav1.OwnerReference{{UID: "rs-uid"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	foreign := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p2", Namespace: "ns", Labels: map[string]string{"app": "x"},
			OwnerReferences: []metav1.OwnerReference{{UID: "other-rs-uid"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	kc := fake.NewSimpleClientset(dep, rs, pod, foreign)

	if err := patchPodsAllocation(context.Background(), kc, "ns", "dep", "H100", 64); err != nil {
		t.Fatalf("patchPodsAllocation: %v", err)
	}
	got, _ := kc.CoreV1().Pods("ns").Get(context.Background(), "p1", metav1.GetOptions{})
	if got.Labels[ctrl.KeyMaxBatchSize] != "64" || got.Labels[ctrl.KeyAccelerator] != "H100" {
		t.Fatalf("owned pod labels not set: %v", got.Labels)
	}
	gotForeign, _ := kc.CoreV1().Pods("ns").Get(context.Background(), "p2", metav1.GetOptions{})
	if _, ok := gotForeign.Labels[ctrl.KeyMaxBatchSize]; ok {
		t.Fatalf("foreign pod was relabelled: %v", gotForeign.Labels)
	}
}
