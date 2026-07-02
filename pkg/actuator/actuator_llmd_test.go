package actuator

import (
	"context"
	"testing"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReplicasPatch(t *testing.T) {
	got := string(replicasPatch(3))
	want := `[{"op":"replace","path":"/spec/replicas","value":3}]`
	if got != want {
		t.Errorf("replicasPatch(3) = %s, want %s", got, want)
	}
}

func TestLLMDActuateScalesReplicasOnly(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "infer",
			Labels: map[string]string{"a": "1"}},
		Spec: appsv1.DeploymentSpec{Replicas: int32ptr(1)},
	}
	kc := fake.NewSimpleClientset(dep)
	a := newLLMDActuator()

	u := backend.DeploymentUpdate{
		ServerName: "qwen", DeployName: "qwen", Namespace: "infer",
		Allocation: config.AllocationData{NumReplicas: 4, Accelerator: "H100", MaxBatch: 256},
	}
	if err := a.Actuate(context.Background(), kc, u); err != nil {
		t.Fatalf("Actuate: %v", err)
	}
	out, _ := kc.AppsV1().Deployments("infer").Get(context.Background(), "qwen", metav1.GetOptions{})
	if out.Spec.Replicas == nil || *out.Spec.Replicas != 4 {
		t.Errorf("replicas = %v, want 4", out.Spec.Replicas)
	}
	// llmd must NOT write accelerator/maxbatchsize labels
	if _, ok := out.Labels["inferno.server.allocation.accelerator"]; ok {
		t.Errorf("llmd actuator wrote accelerator label; should not")
	}
}

func int32ptr(n int32) *int32 { return &n }
