package collector

import "testing"

func TestBuildReplicaSpecCoherent(t *testing.T) {
	env := &latestEnvelope{
		EffectiveInput: simRequest{RPS: 5, MaxConcurrency: 32, AvgInputTokens: 1024, AvgOutputTokens: 512},
		Result:         simResult{AvgITL: 11, AvgTTFT: 120, Throughput: 4},
	}
	spec, ok := buildReplicaSpec("srv", "pod-1", "Bronze", "m", 64, 32, "H100", env)
	if !ok {
		t.Fatal("ok=false, want true (concurrency matches)")
	}
	if spec.Name != "srv/pod-1" {
		t.Fatalf("name = %q", spec.Name)
	}
	if spec.CurrentAlloc.ITLAverage != 11 || spec.CurrentAlloc.TTFTAverage != 120 {
		t.Fatalf("latency wrong: %+v", spec.CurrentAlloc)
	}
	if spec.CurrentAlloc.Load.ArrivalRate != 300 || spec.CurrentAlloc.Load.Throughput != 240 {
		t.Fatalf("load wrong: %+v", spec.CurrentAlloc.Load) // 5*60=300, 4*60=240
	}
	if spec.CurrentAlloc.MaxBatch != 32 || spec.CurrentAlloc.NumReplicas != 1 {
		t.Fatalf("alloc wrong: %+v", spec.CurrentAlloc)
	}
}

func TestBuildReplicaSpecStaleConcurrencyMismatch(t *testing.T) {
	env := &latestEnvelope{EffectiveInput: simRequest{MaxConcurrency: 32}}
	if _, ok := buildReplicaSpec("srv", "p", "c", "m", 64, 128 /*in-force differs*/, "H100", env); ok {
		t.Fatal("ok=true, want false (stale: 32 != 128)")
	}
}

func TestBuildReplicaSpecNilEnv(t *testing.T) {
	if _, ok := buildReplicaSpec("srv", "p", "c", "m", 64, 32, "H100", nil); ok {
		t.Fatal("ok=true, want false (nil env)")
	}
}

func TestBuildReplicaSpecZeroInForceSkips(t *testing.T) {
	// inForceMaxBatch=0 means no allocation is in force yet; even if env also
	// reports MaxConcurrency=0 (a 0==0 coincidence), the pod must be skipped.
	env := &latestEnvelope{EffectiveInput: simRequest{MaxConcurrency: 0}}
	if _, ok := buildReplicaSpec("srv", "p", "c", "m", 64, 0 /*inForceMaxBatch*/, "H100", env); ok {
		t.Fatal("ok=true, want false (zero in-force allocation must not pass coherence)")
	}
}
