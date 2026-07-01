package controller

import (
	"fmt"
	"os"
	"time"

	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// calibrationEnabled reports whether benchmarking-on-the-fly calibration is turned on.
func calibrationEnabled() bool {
	v := os.Getenv(CalibrationEnabledEnvName)
	return v == "true" || v == "1"
}

// maybeCalibrate runs the benchmarking-on-the-fly trigger. For each (model, accelerator) the tuner
// reports as needing calibration — natural warm-up excitation was insufficient (ill-conditioned
// fit) and the pair has not been calibrated yet — it asks the Collector to run a short load sweep
// against a backing pod and feeds the measured operating points to the tuner's /calibrate. On
// success the tuner stores an identifiable, graduated fit, so the warm-up gate clears and the
// subsequent /merge injects the calibrated parameters this same cycle.
//
// Best-effort: every failure is logged and the cycle proceeds with current model data. Runs inline
// (the control mutex is held); the blis sweep is fast (synchronous on-demand /simulate). Returns
// true if at least one pair was calibrated.
func (a *Controller) maybeCalibrate(spec []config.ServerSpec) bool {
	statuses, err := GETCalibrationStatus()
	if err != nil {
		fmt.Printf("%v: calibration status warning (skipping calibration): %s\n",
			time.Now().Format("15:04:05.000"), err.Error())
		return false
	}

	calibratedAny := false
	for _, st := range statuses {
		if !st.NeedsCalibration {
			continue
		}
		server := serverForModelAccel(spec, st.Model, st.Accelerator)
		if server == "" {
			fmt.Printf("%v: calibration: no managed server for %s/%s; skipping\n",
				time.Now().Format("15:04:05.000"), st.Model, st.Accelerator)
			continue
		}
		fmt.Printf("%v: calibration: %s/%s ill-conditioned (kappa=%.3g) — sweeping server %s\n",
			time.Now().Format("15:04:05.000"), st.Model, st.Accelerator, st.ConditionNumber, server)

		points, err := GETSweep(server)
		if err != nil {
			fmt.Printf("%v: calibration sweep failed for %s: %s\n",
				time.Now().Format("15:04:05.000"), server, err.Error())
			continue
		}
		if len(points) < 2 {
			fmt.Printf("%v: calibration sweep for %s returned %d usable points (<2); skipping\n",
				time.Now().Format("15:04:05.000"), server, len(points))
			continue
		}
		if _, err := POSTCalibrate(points); err != nil {
			fmt.Printf("%v: calibration /calibrate failed for %s/%s: %s\n",
				time.Now().Format("15:04:05.000"), st.Model, st.Accelerator, err.Error())
			continue
		}
		fmt.Printf("%v: calibration complete for %s/%s (%d points)\n",
			time.Now().Format("15:04:05.000"), st.Model, st.Accelerator, len(points))
		calibratedAny = true
	}
	return calibratedAny
}

// serverForModelAccel returns the managed server name backing a (model, accelerator) pair, from the
// just-collected server specs (the Collector indexes sweeps by server name, the tuner by pair).
func serverForModelAccel(spec []config.ServerSpec, model, accelerator string) string {
	for _, s := range spec {
		if s.Model == model && s.CurrentAlloc.Accelerator == accelerator {
			return s.Name
		}
	}
	return ""
}
