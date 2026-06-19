package monitor

import (
	"testing"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

func TestBuildRecordOccupancy(t *testing.T) {
	servers := []config.ServerSpec{{
		Name:  "srv",
		Class: "Bronze",
		Model: "m",
		CurrentAlloc: config.AllocationData{
			NumReplicas:    3,
			AvgConcurrency: 2.5, // per-replica
		},
	}}
	rec := BuildRecord(1, servers, nil, nil, config.ModelData{}, config.CapacityData{}, 0, 0, 0, 0, 0)
	if len(rec.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(rec.Servers))
	}
	sr := rec.Servers[0]
	if sr.OccPerReplica != 2.5 {
		t.Fatalf("OccPerReplica = %v, want 2.5", sr.OccPerReplica)
	}
	if sr.OccTotal != 7.5 { // 2.5 * 3 replicas (observed current count)
		t.Fatalf("OccTotal = %v, want 7.5", sr.OccTotal)
	}
}
