package monitor

// CycleRecord is one JSON line written per control cycle.
type CycleRecord struct {
	Timestamp string                      `json:"ts"`        // RFC3339Nano
	Cycle     int64                       `json:"cycle"`     // monotonically increasing counter
	Servers   []ServerRecord              `json:"servers"`   // per-deployment metrics
	Internals []ModelParms                `json:"internals"` // EKF-tuned model parameters
	Capacity  []AcceleratorCapacityRecord `json:"capacity"`  // allocated vs available per accelerator type
	TotalCost float32                     `json:"totalCost"` // sum of all server costs
	Timing    TimingRecord                `json:"timing"`    // cycle phase durations in ms
}

// ServerRecord holds per-deployment metrics for one cycle.
type ServerRecord struct {
	// Identity
	Name  string `json:"name"`
	Class string `json:"class"`
	Model string `json:"model"`

	// Workload
	ArrivalRate  float32 `json:"rpm"`        // req/min (arrival rate)
	Throughput   float32 `json:"throughput"` // req/min (completed)
	AvgInTokens  int     `json:"avgInTok"`
	AvgOutTokens int     `json:"avgOutTok"`

	// Performance: observed attained values vs SLO targets
	ITL      float32 `json:"itl"`      // attained average ITL (ms)
	TTFT     float32 `json:"ttft"`     // attained average TTFT (ms)
	SLO_ITL  float32 `json:"sloItl"`   // SLO target ITL (ms)
	SLO_TTFT float32 `json:"sloTtft"` // SLO target TTFT (ms)

	// Controls: optimizer decisions
	Accelerator string  `json:"accelerator"`
	NumReplicas int     `json:"replicas"`
	Cost        float32 `json:"cost"`
}

// AcceleratorCapacityRecord holds allocated vs available counts for one accelerator type.
type AcceleratorCapacityRecord struct {
	Type      string `json:"type"`      // accelerator type name (e.g. "H100")
	Allocated int    `json:"allocated"` // number of accelerator units in use
	Available int    `json:"available"` // total number of units in the cluster
}

// ModelParms holds EKF-estimated latency model parameters for one model/accelerator pair.
type ModelParms struct {
	Model string  `json:"model"`
	Acc   string  `json:"acc"`
	Alpha float32 `json:"alpha"` // base overhead (ms)
	Beta  float32 `json:"beta"`  // compute time scaling
	Gamma float32 `json:"gamma"` // memory access time scaling
}

// TimingRecord holds the duration of each phase in the control cycle.
type TimingRecord struct {
	CollectMs  int64 `json:"collectMs"`
	TuneMs     int64 `json:"tuneMs"`
	OptimizeMs int64 `json:"optimizeMs"`
	ActuateMs  int64 `json:"actuateMs"`
	TotalMs    int64 `json:"totalMs"`
}
