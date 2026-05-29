package actuator

import (
	"context"
	"fmt"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// reconcileOne runs one reconcile pass for a single managed Deployment.
// It returns nil on success or skip; non-nil only on programming errors.
// Transient K8s API errors are logged and swallowed — the next tick is the retry.
func reconcileOne(ctx context.Context, kc kubernetes.Interface, managed *appsv1.Deployment) error {
	// Opt-in: only act on managed Deployments labelled vllm-server.
	if managed.Labels[ctrl.KeyEvaluator] != ctrl.EvaluatorVLLMServer {
		return nil
	}

	vName := managed.Labels[ctrl.KeyVLLMDeployment]
	if vName == "" {
		fmt.Printf("pairing: managed Deployment %s/%s has evaluator=vllm-server but no %s label; skipping\n",
			managed.Namespace, managed.Name, ctrl.KeyVLLMDeployment)
		return nil
	}
	vNs := managed.Labels[ctrl.KeyVLLMNamespace]
	if vNs == "" {
		vNs = managed.Namespace
	}

	vllm, err := kc.AppsV1().Deployments(vNs).Get(ctx, vName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("pairing: vllm Deployment %s/%s not found: %v; skipping\n", vNs, vName, err)
		return nil
	}

	// Invariant 1+4: replica lockstep, vllm scaled first.
	managedRep := int32(0)
	if managed.Spec.Replicas != nil {
		managedRep = *managed.Spec.Replicas
	}
	vllmRep := int32(0)
	if vllm.Spec.Replicas != nil {
		vllmRep = *vllm.Spec.Replicas
	}
	if vllmRep != managedRep {
		fmt.Printf("pairing: scaling vllm %s/%s replicas %d -> %d\n", vNs, vName, vllmRep, managedRep)
		if err := setDeploymentReplicas(ctx, kc, vNs, vName, managedRep); err != nil {
			fmt.Printf("pairing: setDeploymentReplicas: %v\n", err)
		}
		// Let the next tick observe Ready transitions before labelling.
		return nil
	}

	// Snapshot Ready pods on each side.
	mPods, err := listOwnedReadyPods(ctx, kc, managed, ctrl.KeyPairID)
	if err != nil {
		fmt.Printf("pairing: list managed pods: %v\n", err)
		return nil
	}
	vPods, err := listOwnedReadyPods(ctx, kc, vllm, ctrl.KeyPairID)
	if err != nil {
		fmt.Printf("pairing: list vllm pods: %v\n", err)
		return nil
	}

	// Compute and apply.
	plan := ComputePairingPatches(mPods, vPods, func() string { return uuid.NewString() })

	for _, p := range plan.Prunes {
		if err := removePodLabel(ctx, kc, p.Namespace, p.Name, ctrl.KeyPairID); err != nil {
			fmt.Printf("pairing: removePodLabel %s/%s: %v\n", p.Namespace, p.Name, err)
		}
	}
	for _, b := range plan.Bindings {
		if err := setPodLabel(ctx, kc, b.Managed.Namespace, b.Managed.Name, ctrl.KeyPairID, b.UUID); err != nil {
			fmt.Printf("pairing: setPodLabel managed %s/%s: %v\n", b.Managed.Namespace, b.Managed.Name, err)
			continue
		}
		if err := setPodLabel(ctx, kc, b.VLLM.Namespace, b.VLLM.Name, ctrl.KeyPairID, b.UUID); err != nil {
			fmt.Printf("pairing: setPodLabel vllm %s/%s: %v\n", b.VLLM.Namespace, b.VLLM.Name, err)
		}
		fmt.Printf("pairing: bound %s/%s <-> %s/%s with id %s\n",
			b.Managed.Namespace, b.Managed.Name, b.VLLM.Namespace, b.VLLM.Name, b.UUID)
	}
	return nil
}

// reconcileAll lists all managed Deployments and runs reconcileOne on each.
func reconcileAll(ctx context.Context, kc kubernetes.Interface) {
	deps, err := kc.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: ctrl.KeyManaged + "=true",
	})
	if err != nil {
		fmt.Printf("pairing: list managed Deployments: %v\n", err)
		return
	}
	for i := range deps.Items {
		_ = reconcileOne(ctx, kc, &deps.Items[i])
	}
}

// runReconciler runs reconcileAll on a ticker until ctx is cancelled.
func runReconciler(ctx context.Context, kc kubernetes.Interface, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcileAll(ctx, kc)
		}
	}
}
