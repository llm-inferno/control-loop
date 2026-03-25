package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
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

// simulatePod calls POST /simulate on the server-sim sidecar via the k8s API
// server proxy (works from inside and outside the cluster), then polls
// GET /simulate/:id until the job completes or times out.
func simulatePod(kubeClient *kubernetes.Clientset, namespace, podName string, port int, req simRequest) (*simResult, error) {
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
		DoRaw(context.TODO())
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

	// Poll GET /simulate/:id via k8s API proxy
	deadline := time.Now().Add(simTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(simPollInterval)
		data, err := kubeClient.CoreV1().RESTClient().Get().
			Namespace(namespace).
			Resource("pods").
			Name(fmt.Sprintf("%s:%d", podName, port)).
			SubResource("proxy").
			Suffix("/simulate/" + jobResp.JobID).
			DoRaw(context.TODO())
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
