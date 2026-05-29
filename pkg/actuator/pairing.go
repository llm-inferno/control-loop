package actuator

import (
	"context"
	"fmt"
	"sync"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// pairingLogDedup deduplicates noisy per-Deployment log lines so each unique
// (deploymentKey, message) pair is logged at most once per dedupTTL.
type pairingLogDedupEntry struct {
	msg string
	at  time.Time
}

var (
	pairingLogMu   sync.Mutex
	pairingLogSeen = map[string]pairingLogDedupEntry{}
	pairingLogTTL  = time.Minute
)

// logOncePerMinute prints msg at most once per pairingLogTTL per key.
// key is typically "<namespace>/<name>" of the managed Deployment.
func logOncePerMinute(key, msg string) {
	pairingLogMu.Lock()
	defer pairingLogMu.Unlock()
	now := time.Now()
	if e, ok := pairingLogSeen[key]; ok && e.msg == msg && now.Sub(e.at) < pairingLogTTL {
		return
	}
	pairingLogSeen[key] = pairingLogDedupEntry{msg: msg, at: now}
	fmt.Println(msg)
}

// reconcileOne runs one reconcile pass for a single managed Deployment.
// It always returns nil; transient K8s API errors are logged and swallowed
// since the next tick is the retry (see spec §7). The error return is kept
// for forward compatibility if future logic needs to escalate.
func reconcileOne(ctx context.Context, kc kubernetes.Interface, managed *appsv1.Deployment) error {
	// Opt-in: only act on managed Deployments labelled vllm-server.
	if managed.Labels[ctrl.KeyEvaluator] != ctrl.EvaluatorVLLMServer {
		return nil
	}

	vName := managed.Labels[ctrl.KeyVLLMDeployment]
	if vName == "" {
		key := managed.Namespace + "/" + managed.Name
		logOncePerMinute(key, fmt.Sprintf("pairing: managed Deployment %s has evaluator=vllm-server but no %s label; skipping",
			key, ctrl.KeyVLLMDeployment))
		return nil
	}
	vNs := managed.Labels[ctrl.KeyVLLMNamespace]
	if vNs == "" {
		vNs = managed.Namespace
	}

	vllm, err := kc.AppsV1().Deployments(vNs).Get(ctx, vName, metav1.GetOptions{})
	if err != nil {
		key := managed.Namespace + "/" + managed.Name
		logOncePerMinute(key, fmt.Sprintf("pairing: vllm Deployment %s/%s not found: %v; skipping", vNs, vName, err))
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
			key := managed.Namespace + "/" + managed.Name
			logOncePerMinute(key, fmt.Sprintf("pairing: setDeploymentReplicas %s/%s: %v", vNs, vName, err))
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
			// Managed pod now carries the UUID but vLLM pod does not. The next tick
			// detects this as an orphaned UUID on the managed side, prunes it, and
			// re-pairs both pods — a ~1-tick window where the evaluator returns 503.
			// This is the expected self-heal path per spec §7.
			fmt.Printf("pairing: setPodLabel vllm %s/%s: %v\n", b.VLLM.Namespace, b.VLLM.Name, err)
			continue
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
	if pairingDebug {
		fmt.Printf("pairing: tick, %d managed deployment(s)\n", len(deps.Items))
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
