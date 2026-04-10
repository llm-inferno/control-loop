package loademulator

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Phase describes one segment of the load phase sequence.
// Duration is the real-time length of the phase; zero means hold forever (terminal).
// Ratio is the factor by which the nominal RPM changes from the start to the end of this
// phase, relative to the nominal at the start of the phase (chained). Ignored for terminal phases.
type Phase struct {
	Duration time.Duration
	Ratio    float64
}

// phaseEntry is the raw YAML representation of a phase.
type phaseEntry struct {
	Duration string  `yaml:"duration"`
	Ratio    float64 `yaml:"ratio"`
}

// phaseFile is the top-level YAML structure.
type phaseFile struct {
	Phases []phaseEntry `yaml:"phases"`
}

// LoadPhasesFromFile parses a YAML phase config file and returns a PhaseTracker.
// Returns nil, nil when path is empty (feature disabled).
func LoadPhasesFromFile(path string) (*PhaseTracker, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("phases: reading %s: %w", path, err)
	}
	var f phaseFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("phases: parsing %s: %w", path, err)
	}
	if len(f.Phases) == 0 {
		return nil, fmt.Errorf("phases: %s has no phases defined", path)
	}
	phases := make([]Phase, 0, len(f.Phases))
	for i, e := range f.Phases {
		d, err := time.ParseDuration(e.Duration)
		if err != nil {
			return nil, fmt.Errorf("phases: entry %d: invalid duration %q: %w", i, e.Duration, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("phases: entry %d: duration must be >= 0, got %s", i, e.Duration)
		}
		if d == 0 && i != len(f.Phases)-1 {
			return nil, fmt.Errorf("phases: entry %d: duration=0 (terminal) must be the last entry", i)
		}
		if d == 0 && e.Ratio != 0 {
			fmt.Printf("phases: entry %d: warning: ratio %.4f is ignored for terminal (duration=0) phases\n", i, e.Ratio)
		}
		if d > 0 && e.Ratio <= 0 {
			return nil, fmt.Errorf("phases: entry %d: ratio must be > 0, got %g", i, e.Ratio)
		}
		phases = append(phases, Phase{Duration: d, Ratio: e.Ratio})
	}
	return &PhaseTracker{phases: phases}, nil
}

// PhaseTracker tracks the current position in the phase sequence and
// returns a cumulative multiplier to apply to the original nominal RPM.
type PhaseTracker struct {
	phases    []Phase
	startTime time.Time
	started   bool
	lastPhase int // last reported phase index, for transition logging
}

// GetMultiplier returns the current cumulative RPM multiplier and the 1-based
// index of the active phase. It initialises the clock on the first call.
func (pt *PhaseTracker) GetMultiplier() (float64, int) {
	if !pt.started {
		pt.startTime = time.Now()
		pt.started = true
	}
	elapsed := time.Since(pt.startTime)

	cumMult := 1.0
	cumTime := time.Duration(0)

	for i, p := range pt.phases {
		phaseNum := i + 1

		// Terminal phase: hold at cumMult forever.
		if p.Duration == 0 {
			pt.logTransition(phaseNum, cumMult)
			return cumMult, phaseNum
		}

		phaseEnd := cumTime + p.Duration
		if elapsed < phaseEnd {
			// We are inside this phase.
			pt.logTransition(phaseNum, cumMult)
			fraction := float64(elapsed-cumTime) / float64(p.Duration)
			endMult := cumMult * p.Ratio
			return cumMult + fraction*(endMult-cumMult), phaseNum
		}

		// Phase fully elapsed: apply its full ratio and advance.
		cumMult *= p.Ratio
		cumTime = phaseEnd
	}

	// Past all phases: hold at final cumMult.
	finalPhase := len(pt.phases)
	if finalPhase != pt.lastPhase {
		fmt.Printf("phases: past all phases, holding at multiplier=%.4f\n", cumMult)
		pt.lastPhase = finalPhase
	}
	return cumMult, finalPhase
}

// logTransition prints a message when the active phase changes.
func (pt *PhaseTracker) logTransition(phaseNum int, mult float64) {
	if phaseNum != pt.lastPhase {
		fmt.Printf("phases: entering phase %d (multiplier=%.4f)\n", phaseNum, mult)
		pt.lastPhase = phaseNum
	}
}

// LogConfig prints the parsed phase sequence to stdout.
func (pt *PhaseTracker) LogConfig() {
	fmt.Printf("phases: loaded %d phase(s):\n", len(pt.phases))
	cumMult := 1.0
	for i, p := range pt.phases {
		if p.Duration == 0 {
			fmt.Printf("  phase %d: hold forever (multiplier=%.4f)\n", i+1, cumMult)
		} else {
			endMult := cumMult * p.Ratio
			fmt.Printf("  phase %d: duration=%s ratio=%.4f multiplier %.4f -> %.4f\n",
				i+1, p.Duration, p.Ratio, cumMult, endMult)
			cumMult = endMult
		}
	}
}
