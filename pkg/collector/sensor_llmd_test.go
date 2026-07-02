package collector

import (
	"math"
	"testing"
)

func approxEq(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-2 }

// fullReading builds a podReading with all four metrics present.
func fullReading(tput, itl, ttft, occ float64) *podReading {
	return &podReading{
		throughputRPM: tput, itlSec: itl, ttftSec: ttft, occupancy: occ,
		hasThroughput: true, hasITL: true, hasTTFT: true, hasOcc: true,
	}
}

func TestBuildLLMDSpecs_TwoPods(t *testing.T) {
	m := llmdMetrics{
		pods: map[string]*podReading{
			"pod-a": fullReading(120, 0.010, 0.100, 4),
			"pod-b": fullReading(60, 0.020, 0.200, 2),
		},
		inTokens:  512,
		outTokens: 128,
	}
	server, replicas := buildLLMDSpecs("srv", "Premium", "qwen3_32b", "H100",
		0, 2, []string{"pod-a", "pod-b"}, m)

	if len(replicas) != 2 {
		t.Fatalf("replicas = %d, want 2", len(replicas))
	}
	// deployment throughput = sum = 180 RPM; ArrivalRate := Throughput
	if !approxEq(server.CurrentAlloc.Load.Throughput, 180) {
		t.Errorf("throughput = %v, want 180", server.CurrentAlloc.Load.Throughput)
	}
	if server.CurrentAlloc.Load.ArrivalRate != server.CurrentAlloc.Load.Throughput {
		t.Errorf("ArrivalRate %v != Throughput %v", server.CurrentAlloc.Load.ArrivalRate, server.CurrentAlloc.Load.Throughput)
	}
	// ITL is throughput-weighted: (0.010*120 + 0.020*60)/180 s = 0.01333 s -> 13.33 ms
	if !approxEq(server.CurrentAlloc.ITLAverage, 13.333) {
		t.Errorf("ITL = %v ms, want ~13.33", server.CurrentAlloc.ITLAverage)
	}
	// TTFT weighted: (0.100*120 + 0.200*60)/180 = 0.13333 s -> 133.33 ms
	if !approxEq(server.CurrentAlloc.TTFTAverage, 133.333) {
		t.Errorf("TTFT = %v ms, want ~133.33", server.CurrentAlloc.TTFTAverage)
	}
	// occupancy is mean over reporting pods: (4+2)/2 = 3
	if !approxEq(server.CurrentAlloc.AvgConcurrency, 3) {
		t.Errorf("occ = %v, want 3", server.CurrentAlloc.AvgConcurrency)
	}
	if server.CurrentAlloc.Load.AvgInTokens != 512 || server.CurrentAlloc.Load.AvgOutTokens != 128 {
		t.Errorf("tokens = %d/%d, want 512/128", server.CurrentAlloc.Load.AvgInTokens, server.CurrentAlloc.Load.AvgOutTokens)
	}
	if server.CurrentAlloc.NumReplicas != 2 {
		t.Errorf("numReplicas = %d, want 2", server.CurrentAlloc.NumReplicas)
	}
}

func TestBuildLLMDSpecs_NoReporting(t *testing.T) {
	m := newLLMDMetrics()
	server, replicas := buildLLMDSpecs("srv", "Premium", "qwen3_32b", "H100", 0, 1, nil, m)
	if len(replicas) != 0 {
		t.Errorf("replicas = %d, want 0", len(replicas))
	}
	if server.CurrentAlloc.Load.Throughput != 0 || server.CurrentAlloc.ITLAverage != 0 {
		t.Errorf("expected zeroed alloc, got %+v", server.CurrentAlloc)
	}
}

// A pod that reports throughput but is missing its ITL/TTFT reading this window
// (vLLM's 0/0→NaN ratio, dropped upstream) must not be folded into the
// throughput-weighted latency average as a spurious 0 — that would deflate the
// deployment latency the optimizer reads. The average should reflect only the
// pod that actually reported latency.
func TestBuildLLMDSpecs_MissingLatencyNotDeflated(t *testing.T) {
	m := llmdMetrics{
		pods: map[string]*podReading{
			"pod-a": fullReading(120, 0.010, 0.100, 4),
			// pod-b: throughput present, but ITL/TTFT/occ absent this window.
			"pod-b": {throughputRPM: 60, hasThroughput: true},
		},
	}
	server, replicas := buildLLMDSpecs("srv", "Bronze", "qwen3_32b", "H100",
		0, 2, []string{"pod-a", "pod-b"}, m)

	// Both pods report throughput → deployment throughput sums to 180.
	if !approxEq(server.CurrentAlloc.Load.Throughput, 180) {
		t.Errorf("throughput = %v, want 180 (both pods offer load)", server.CurrentAlloc.Load.Throughput)
	}
	if len(replicas) != 2 {
		t.Fatalf("replicas = %d, want 2", len(replicas))
	}
	// Latency is weighted only over pod-a (the sole reporter): 10ms / 100ms, NOT
	// pulled toward 0 by pod-b's absent reading.
	if !approxEq(server.CurrentAlloc.ITLAverage, 10) {
		t.Errorf("ITL = %v ms, want 10 (pod-b's missing ITL must not deflate)", server.CurrentAlloc.ITLAverage)
	}
	if !approxEq(server.CurrentAlloc.TTFTAverage, 100) {
		t.Errorf("TTFT = %v ms, want 100", server.CurrentAlloc.TTFTAverage)
	}
	// Occupancy mean is over pods that reported occupancy (only pod-a) → 4.
	if !approxEq(server.CurrentAlloc.AvgConcurrency, 4) {
		t.Errorf("occ = %v, want 4", server.CurrentAlloc.AvgConcurrency)
	}
}

func TestSanitizeGuardsInf(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"positive inf", math.Inf(1), 0},
		{"negative inf", math.Inf(-1), 0},
		{"nan", math.NaN(), 0},
		{"normal", 3.5, 3.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitize(tt.in)
			if math.IsNaN(tt.want) {
				if !math.IsNaN(got) {
					t.Errorf("sanitize(%v) = %v, want NaN", tt.in, got)
				}
			} else if got != tt.want {
				t.Errorf("sanitize(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
