package collector

import "testing"

func TestSelectArrivalRate(t *testing.T) {
	const (
		offered  = 672.0  // partial measured Σ (under-count)
		setpoint = 1250.0 // true deployment offered (load.rpm label)
		prom     = 500.0  // Prometheus/static backup
	)
	tests := []struct {
		name          string
		numReporting  int
		numReplicas   int
		totalOffered  float64
		setpoint      float64
		arvRate       float64
		hasSetpoint   bool
		want          float64
	}{
		{"full reporting uses measured sum", 5, 5, offered, setpoint, prom, true, offered},
		{"partial reporting prefers setpoint label", 3, 5, offered, setpoint, prom, true, setpoint},
		{"zero reporting prefers setpoint label", 0, 5, 0, setpoint, prom, true, setpoint},
		{"partial reporting no label falls back to partial sum", 3, 5, offered, 0, prom, false, offered},
		{"zero reporting no label falls back to arvRate", 0, 5, 0, 0, prom, false, prom},
		{"scale-in over-report prefers setpoint label", 4, 3, offered, setpoint, prom, true, setpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectArrivalRate(tt.numReporting, tt.numReplicas,
				tt.totalOffered, tt.setpoint, tt.arvRate, tt.hasSetpoint)
			if got != tt.want {
				t.Fatalf("selectArrivalRate = %v, want %v", got, tt.want)
			}
		})
	}
}
