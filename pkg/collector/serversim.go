package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"
)

// SimulateTimeoutEnvName overrides the per-pod GET /latest timeout (seconds).
// In continuous mode the Collector does a non-blocking read of the most-recent
// completed window, so this timeout bounds only the k8s API-server proxy
// round-trip — not an evaluation window. The 30s default is ample.
const SimulateTimeoutEnvName = "INFERNO_SIMULATE_TIMEOUT_SEC"

const defaultSimTimeout = 30 * time.Second

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

// simResult holds the /latest result envelope. The collector consumes
// AvgITL, AvgTTFT, Throughput, AvgRespTime, and AvgWaitTime (the latter two
// drive the Little's-Law in-service occupancy computed in buildReplicaSpec);
// the remaining fields (Saturation, MaxRPS) are decoded for wire-contract
// completeness.
type simResult struct {
	Throughput  float32 `json:"throughput"`
	AvgRespTime float32 `json:"avgRespTime"`
	AvgWaitTime float32 `json:"avgWaitTime"`
	AvgTTFT     float32 `json:"avgTTFT"`
	AvgITL      float32 `json:"avgITL"`
	MaxRPS      float32 `json:"maxRPS"`
	Saturation  string  `json:"saturation,omitempty"`
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

// jobCreated is the server-sim POST /simulate response (async job id).
type jobCreated struct {
	JobID string `json:"jobID"`
}

// jobStatus is the server-sim GET /simulate/:id response. Status is one of
// "pending", "completed", "failed" (pkg/job.Status).
type jobStatus struct {
	Status string    `json:"status"`
	Result simResult `json:"result"`
	Error  string    `json:"error"`
}

// runSimulate drives one on-demand simulation at a specific operating point against a pod's
// server-sim sidecar, used by the benchmarking-on-the-fly calibration sweep. It POSTs the
// operating point to server-sim's async POST /simulate via the k8s API-server proxy, then polls
// GET /simulate/:id until the job completes or fails. Unlike getLatest (which reads the in-force
// labels), this drives an arbitrary (RPS, tokens, concurrency) point — the sweep's persistent
// excitation. timeout bounds the whole point (create + poll); pollInterval paces the status reads.
func runSimulate(kubeClient *kubernetes.Clientset, namespace, podName string, port int,
	req simRequest, timeout, pollInterval time.Duration) (*simResult, error) {

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal /simulate request: %w", err)
	}

	raw, err := kubeClient.CoreV1().RESTClient().Post().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, port)).
		SubResource("proxy").
		Suffix("/simulate").
		Body(body).
		SetHeader("Content-Type", "application/json").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("POST /simulate: %w", err)
	}
	var created jobCreated
	if err := json.Unmarshal(raw, &created); err != nil || created.JobID == "" {
		return nil, fmt.Errorf("decode /simulate jobID (body=%q): %w", string(raw), err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	suffix := "/simulate/" + created.JobID
	for {
		data, err := kubeClient.CoreV1().RESTClient().Get().
			Namespace(namespace).
			Resource("pods").
			Name(fmt.Sprintf("%s:%d", podName, port)).
			SubResource("proxy").
			Suffix(suffix).
			DoRaw(ctx)
		if err != nil {
			// A transient k8s API-server-proxy blip should not discard this
			// calibration point: retry on the next tick until the point's
			// timeout budget (ctx) is exhausted. A persistent failure still
			// surfaces via ctx.Done() below.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("GET %s: %w", suffix, err)
			}
			fmt.Printf("runSimulate: transient poll error for job %s (retrying): %v\n", created.JobID, err)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("simulate job %s timed out after %s", created.JobID, timeout)
			case <-ticker.C:
			}
			continue
		}
		var js jobStatus
		if err := json.Unmarshal(data, &js); err != nil {
			return nil, fmt.Errorf("decode job status (body=%q): %w", string(data), err)
		}
		switch js.Status {
		case "completed":
			result := js.Result
			return &result, nil
		case "failed":
			return nil, fmt.Errorf("simulate job %s failed: %s", created.JobID, js.Error)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("simulate job %s timed out after %s", created.JobID, timeout)
		case <-ticker.C:
		}
	}
}
