package monitor

import (
	"time"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// BuildRecord assembles a CycleRecord from the data available at the end of a control cycle.
// servers: deployment-level specs from the collector (observed state).
// solution: optimizer allocation decisions (decided state).
// serviceClasses: SLO targets, matched by server class and model.
// modelData: current EKF-tuned model performance parameters.
func BuildRecord(
	cycle int64,
	servers []config.ServerSpec,
	solution map[string]config.AllocationData,
	serviceClasses []config.ServiceClassSpec,
	modelData config.ModelData,
	collectMs, tuneMs, optimizeMs, actuateMs, totalMs int64,
) *CycleRecord {
	serverRecords := make([]ServerRecord, 0, len(servers))
	var totalCost float32

	for _, s := range servers {
		sr := ServerRecord{
			Name:         s.Name,
			Class:        s.Class,
			Model:        s.Model,
			ArrivalRate:  s.CurrentAlloc.Load.ArrivalRate,
			Throughput:   s.CurrentAlloc.Load.Throughput,
			AvgInTokens:  s.CurrentAlloc.Load.AvgInTokens,
			AvgOutTokens: s.CurrentAlloc.Load.AvgOutTokens,
			ITL:          s.CurrentAlloc.ITLAverage,
			TTFT:         s.CurrentAlloc.TTFTAverage,
		}

		// SLO targets: match service class name then model name
		for _, sc := range serviceClasses {
			if sc.Name == s.Class {
				for _, mt := range sc.ModelTargets {
					if mt.Model == s.Model {
						sr.SLO_ITL = mt.SLO_ITL
						sr.SLO_TTFT = mt.SLO_TTFT
						break
					}
				}
				break
			}
		}

		// Optimizer decisions
		if alloc, ok := solution[s.Name]; ok {
			sr.Accelerator = alloc.Accelerator
			sr.NumReplicas = alloc.NumReplicas
			sr.Cost = alloc.Cost
			totalCost += alloc.Cost
		}

		serverRecords = append(serverRecords, sr)
	}

	// EKF-tuned model parameters
	internals := make([]ModelParms, 0, len(modelData.PerfData))
	for _, pd := range modelData.PerfData {
		internals = append(internals, ModelParms{
			Model: pd.Name,
			Acc:   pd.Acc,
			Alpha: pd.PerfParms.Alpha,
			Beta:  pd.PerfParms.Beta,
			Gamma: pd.PerfParms.Gamma,
		})
	}

	return &CycleRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Cycle:     cycle,
		Servers:   serverRecords,
		Internals: internals,
		TotalCost: totalCost,
		Timing: TimingRecord{
			CollectMs:  collectMs,
			TuneMs:     tuneMs,
			OptimizeMs: optimizeMs,
			ActuateMs:  actuateMs,
			TotalMs:    totalMs,
		},
	}
}
