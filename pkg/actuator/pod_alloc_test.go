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
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dep", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns", Labels: map[string]string{"app": "x"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	kc := fake.NewSimpleClientset(dep, pod)

	if err := patchPodsAllocation(context.Background(), kc, "ns", "dep", "H100", 64); err != nil {
		t.Fatalf("patchPodsAllocation: %v", err)
	}
	got, _ := kc.CoreV1().Pods("ns").Get(context.Background(), "p1", metav1.GetOptions{})
	if got.Labels[ctrl.KeyMaxBatchSize] != "64" || got.Labels[ctrl.KeyAccelerator] != "H100" {
		t.Fatalf("pod labels not set: %v", got.Labels)
	}
}
