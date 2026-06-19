package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"
)

// SimulateTimeoutEnvName overrides the per-pod /simulate timeout (seconds).
// Default is 30s, which suffices for queue-analysis and blis (analytical, ms-scale).
// The vllm-server evaluator drives a real vLLM server with a sampling window of
// warmupSec + maxWindowSec (production: 30 + 60–300 = 90–330s); set this env var
// to a value larger than that window for vllm-server runs.
const SimulateTimeoutEnvName = "INFERNO_SIMULATE_TIMEOUT_SEC"

const (
	simPollInitial    = 20 * time.Millisecond
	defaultSimTimeout = 30 * time.Second
)

var simTimeout = defaultSimTimeout

func init() {
	if v := os.Getenv(SimulateTimeoutEnvName); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			simTimeout = time.Duration(secs) * time.Second
		} else {
			fmt.Printf("collector: invalid %s=%q; using default %s\n", SimulateTimeoutEnvName, v, defaultSimTimeout)
		}
	}
}

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
	Saturation  string  `json:"saturation,omitempty"`
}

type simJobResponse struct {
	JobID  string     `json:"jobID"`
	Status string     `json:"status"`
	Result *simResult `json:"result,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// latestEnvelope is the self-describing result served by server-sim GET /latest.
type latestEnvelope struct {
	EffectiveInput simRequest `json:"effectiveInput"`
	Result         simResult  `json:"result"`
	CompletedAt    string     `json:"completedAt"`
}

func parseLatest(data []byte) (*latestEnvelope, error) {
	var env latestEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode /latest: %w", err)
	}
	return &env, nil
}

// getLatest reads the most-recent completed evaluation result from the server-sim
// sidecar via the k8s API-server proxy. Non-blocking: a cold-start 404 (no result
// yet) or any transport error is returned so the caller skips the pod this cycle.
func getLatest(kubeClient *kubernetes.Clientset, namespace, podName string, port int) (*latestEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simTimeout)
	defer cancel()

	data, err := kubeClient.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, port)).
		SubResource("proxy").
		Suffix("/latest").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("GET /latest: %w", err)
	}
	return parseLatest(data)
}

// simulatePod calls POST /simulate on the server-sim sidecar via the k8s API
// server proxy (works from inside and outside the cluster), then polls
// GET /simulate/:id until the job completes or times out.
func simulatePod(kubeClient *kubernetes.Clientset, namespace, podName string, port int, req simRequest) (*simResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simTimeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// POST /simulate via k8s API proxy
	data, err := kubeClient.CoreV1().RESTClient().Post().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, port)).
		SubResource("proxy").
		Suffix("/simulate").
		Body(bytes.NewReader(body)).
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("POST /simulate: %w", err)
	}

	var jobResp struct {
		JobID string `json:"jobID"`
	}
	if err := json.Unmarshal(data, &jobResp); err != nil {
		return nil, fmt.Errorf("decode jobID: %w", err)
	}
	fmt.Printf("simulation job %s submitted\n", jobResp.JobID)

	// Poll GET /simulate/:id via k8s API proxy with exponential backoff
	deadline, _ := ctx.Deadline()
	interval := simPollInitial
	for ctx.Err() == nil {
		remaining := time.Until(deadline)
		if interval > remaining {
			interval = remaining
		}
		time.Sleep(interval)
		interval *= 2
		data, err := kubeClient.CoreV1().RESTClient().Get().
			Namespace(namespace).
			Resource("pods").
			Name(fmt.Sprintf("%s:%d", podName, port)).
			SubResource("proxy").
			Suffix("/simulate/" + jobResp.JobID).
			DoRaw(ctx)
		if err != nil {
			return nil, fmt.Errorf("GET /simulate/%s: %w", jobResp.JobID, err)
		}
		var jr simJobResponse
		if err := json.Unmarshal(data, &jr); err != nil {
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
