package actuator

import (
	"context"
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/backend"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// serversimActuator is today's actuate path: patch replicas + accelerator/
// maxbatchsize labels on the Deployment, then project the maxbatchsize label
// onto each running pod for the server-sim sidecar to read. Behavior-preserving.
type serversimActuator struct{}

func newServerSimActuator() *serversimActuator { return &serversimActuator{} }

func (a *serversimActuator) Actuate(ctx context.Context, kc kubernetes.Interface, u backend.DeploymentUpdate) error {
	if err := patchDeployment(u.ServerName, u.DeployName, u.Namespace, &u.Allocation); err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Printf("srv=[%s/%s]: deployment gone, skipping\n", u.ServerName, u.Namespace)
			return nil
		}
		return err
	}
	if u.Allocation.NumReplicas > 0 {
		if err := patchPodsAllocation(ctx, kc, u.Namespace, u.DeployName,
			u.Allocation.Accelerator, u.Allocation.MaxBatch); err != nil {
			fmt.Printf("srv=[%s/%s]: pod allocation patch warning: %v\n", u.ServerName, u.Namespace, err)
		}
	}
	return nil
}
