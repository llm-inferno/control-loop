package collector

import (
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// buildReplicaSpec maps a /latest envelope to a per-pod ServerSpec. It enforces
// the causal-coherence check: the window's effective concurrency must equal the
// allocation currently in force. A mismatch (the generator has not yet produced
// a window under the new M*) means the observation is stale — return ok=false so
// the caller skips the pod, exactly like a cold-start 404.
func buildReplicaSpec(serverName, podName, class, model string, maxQueueSize, inForceMaxBatch int, accelerator string, env *latestEnvelope) (config.ServerSpec, bool) {
	if env == nil || inForceMaxBatch <= 0 || env.EffectiveInput.MaxConcurrency != inForceMaxBatch {
		return config.ServerSpec{}, false
	}

	// in-service occupancy via Little's Law: throughput(req/s) × in-service time(s).
	// resp−wait can go slightly negative under measurement noise (e.g. vllm: resp and
	// wait come from different request populations) → clamp to 0.
	inServiceMs := env.Result.AvgRespTime - env.Result.AvgWaitTime
	if inServiceMs < 0 {
		inServiceMs = 0
	}
	occ := env.Result.Throughput * inServiceMs / 1000

	return config.ServerSpec{
		Name:         serverName + ctrl.ReplicaNameSeparator + podName,
		Class:        class,
		Model:        model,
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: config.AllocationData{
			Accelerator:    accelerator,
			MaxBatch:       inForceMaxBatch,
			NumReplicas:    1,
			ITLAverage:     env.Result.AvgITL,
			TTFTAverage:    env.Result.AvgTTFT,
			AvgConcurrency: occ,
			Load: config.ServerLoadSpec{
				ArrivalRate:  env.EffectiveInput.RPS * 60,
				Throughput:   env.Result.Throughput * 60,
				AvgInTokens:  int(env.EffectiveInput.AvgInputTokens),
				AvgOutTokens: int(env.EffectiveInput.AvgOutputTokens),
			},
		},
	}, true
}
