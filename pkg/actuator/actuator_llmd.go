package actuator

import (
	"context"
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/backend"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// llmdActuator scales a real llm-d variant Deployment by replicas only. m* is a
// static vLLM startup constraint (--max-num-seq) pinned via DEFAULT_MAX_BATCH_SIZE,
// so there is nothing to project onto pods and no pairing to reconcile.
type llmdActuator struct{}

func newLLMDActuator() *llmdActuator { return &llmdActuator{} }

func replicasPatch(n int) []byte {
	return []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/replicas","value":%d}]`, n))
}

func (a *llmdActuator) Actuate(ctx context.Context, kc kubernetes.Interface, u backend.DeploymentUpdate) error {
	fmt.Printf("srv=[%s/%s]: llmd scale replicas=%d\n", u.ServerName, u.Namespace, u.Allocation.NumReplicas)
	_, err := kc.AppsV1().Deployments(u.Namespace).Patch(ctx, u.DeployName,
		types.JSONPatchType, replicasPatch(u.Allocation.NumReplicas), metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Printf("srv=[%s/%s]: deployment gone, skipping\n", u.ServerName, u.Namespace)
			return nil
		}
		return err
	}
	return nil
}
