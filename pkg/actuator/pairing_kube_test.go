package actuator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSetPodLabel_AddsLabel(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "ns",
			Labels:    map[string]string{"existing": "1"},
		},
	})

	if err := setPodLabel(ctx, kc, "ns", "p1", "inferno.server.pair-id", "uuid-A"); err != nil {
		t.Fatalf("setPodLabel: %v", err)
	}
	got, err := kc.CoreV1().Pods("ns").Get(ctx, "p1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["inferno.server.pair-id"] != "uuid-A" {
		t.Fatalf("label not set: %v", got.Labels)
	}
	if got.Labels["existing"] != "1" {
		t.Fatalf("existing label clobbered: %v", got.Labels)
	}
}
