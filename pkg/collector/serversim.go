package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	simPollInterval = 500 * time.Millisecond
	simTimeout      = 30 * time.Second
)

type simRequest struct {
	RPS             float32 `json:"RPS"`
	MaxConcurrency  int     `json:"maxConcurrency"`
	AvgInputTokens  float32 `json:"avgInputTokens"`
	AvgOutputTokens float32 `json:"avgOutputTokens"`
	Accelerator     string  `json:"accelerator"`
	Model           string  `json:"model"`
}

type simResult struct {
	Throughput  float32 `json:"throughput"`
	AvgRespTime float32 `json:"avgRespTime"`
	AvgWaitTime float32 `json:"avgWaitTime"`
	AvgTTFT     float32 `json:"avgTTFT"`
	AvgITL      float32 `json:"avgITL"`
	MaxRPS      float32 `json:"maxRPS"`
}

type simJobResponse struct {
	JobID  string     `json:"jobID"`
	Status string     `json:"status"`
	Result *simResult `json:"result,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// simulatePod calls POST /simulate on the server-sim sidecar running in the pod,
// then polls GET /simulate/:id until the job completes or times out.
func simulatePod(podIP string, port int, req simRequest) (*simResult, error) {
	baseURL := fmt.Sprintf("http://%s:%d", podIP, port)

	// POST /simulate
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(baseURL+"/simulate", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST /simulate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var jobResp struct {
		JobID string `json:"jobID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		return nil, fmt.Errorf("decode jobID: %w", err)
	}
	fmt.Printf("simulation job %s submitted\n", jobResp.JobID)

	// Poll GET /simulate/:id
	deadline := time.Now().Add(simTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(simPollInterval)
		r, err := http.Get(baseURL + "/simulate/" + jobResp.JobID)
		if err != nil {
			return nil, fmt.Errorf("GET /simulate/%s: %w", jobResp.JobID, err)
		}
		var jr simJobResponse
		err = json.NewDecoder(r.Body).Decode(&jr)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode job response: %w", err)
		}
		switch jr.Status {
		case "completed":
			return jr.Result, nil
		case "failed":
			return nil, fmt.Errorf("simulation failed: %s", jr.Error)
		}
		// pending — keep polling
	}
	return nil, fmt.Errorf("simulation timed out after %s", simTimeout)
}
