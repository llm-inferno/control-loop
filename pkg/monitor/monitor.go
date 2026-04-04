package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const (
	LogPathEnvName = "INFERNO_CYCLE_LOG"
	DefaultLogPath = "inferno-cycles.jsonl"
)

// CycleRecorder appends one JSON line per control cycle to a file.
// A nil CycleRecorder is safe to use — all methods are no-ops.
type CycleRecorder struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// NewCycleRecorder opens the JSONL log file for appending.
// The file path is read from INFERNO_CYCLE_LOG (default: inferno-cycles.jsonl).
// If the env var is set to an empty string, logging is disabled and (nil, nil) is returned.
func NewCycleRecorder() (*CycleRecorder, error) {
	path := os.Getenv(LogPathEnvName)
	if path == "" {
		path = DefaultLogPath
	}
	if path == "-" {
		// explicit sentinel: disable logging
		return nil, nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open cycle log %q: %w", path, err)
	}

	r := &CycleRecorder{f: f, enc: json.NewEncoder(f)}
	r.enc.SetEscapeHTML(false)
	return r, nil
}

// Record writes one CycleRecord as a JSON line. Safe to call on a nil receiver.
func (r *CycleRecorder) Record(rec *CycleRecord) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enc.Encode(rec)
}

// Close closes the underlying log file. Safe to call on a nil receiver.
func (r *CycleRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
