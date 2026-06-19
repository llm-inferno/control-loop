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

// simResult holds the /latest result envelope. The collector consumes only
// AvgITL, AvgTTFT, and Throughput; the remaining fields (Saturation, MaxRPS,
// AvgRespTime, AvgWaitTime) are decoded for wire-contract completeness.
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
