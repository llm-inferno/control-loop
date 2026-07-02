package collector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// podReading holds one pod's real-vLLM readings. The per-field presence flags
// distinguish a genuinely-absent metric from a measured zero: vLLM's ITL/TTFT
// ratios are 0/0→NaN whenever a pod completed nothing in the window, and such a
// pod must NOT be folded into the throughput-weighted latency average as a
// spurious 0 (which would deflate the deployment latency the optimizer reads).
type podReading struct {
	throughputRPM float64 // completions/min
	itlSec        float64 // seconds
	ttftSec       float64 // seconds
	occupancy     float64 // num_requests_running gauge
	hasThroughput bool
	hasITL        bool
	hasTTFT       bool
	hasOcc        bool
}

// llmdMetrics holds per-pod (keyed by pod name) real-vLLM readings plus the
// deployment-level token averages, as fetched from Prometheus/Thanos. Grouping
// the four per-pod series into one map makes the "same pod" invariant structural
// rather than an implicit contract across parallel maps.
type llmdMetrics struct {
	pods      map[string]*podReading
	inTokens  float64
	outTokens float64
}

func newLLMDMetrics() llmdMetrics { return llmdMetrics{pods: map[string]*podReading{}} }

// reading returns pod's reading, creating it on first touch.
func (m *llmdMetrics) reading(pod string) *podReading {
	pr, ok := m.pods[pod]
	if !ok {
		pr = &podReading{}
		m.pods[pod] = pr
	}
	return pr
}

// llmdSensor senses a real llm-d variant entirely from Prometheus. No server-sim
// sidecar, no coherence gate (m* is pinned). ArrivalRate := Throughput because
// this vLLM exports no arrival counter (documented limitation).
type llmdSensor struct {
	api    v1.API
	window string
}

func newLLMDSensor() *llmdSensor {
	apiv1, err := newPromAPI()
	if err != nil {
		log.Fatalf("llmd sensor: prometheus client error: %v", err)
	}
	window := os.Getenv(ctrl.PrometheusWindowEnvName)
	if window == "" {
		window = "1m"
	}
	return &llmdSensor{api: apiv1, window: window}
}

func (s *llmdSensor) Sense(ctx context.Context, d appsv1.Deployment, kc kubernetes.Interface) (
	config.ServerSpec, []config.ServerSpec, error) {

	serverName := d.Labels[ctrl.KeyServerName]
	maxQueueSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxQueueSize])
	numReplicas := 0
	if d.Spec.Replicas != nil {
		numReplicas = int(*d.Spec.Replicas)
	}

	podNames, err := runningPodNames(ctx, kc, d)
	if err != nil {
		return config.ServerSpec{}, nil, err
	}
	if len(podNames) == 0 {
		// nothing reporting this cycle; still emit a zeroed deployment spec so
		// numReplicas/labels flow, mirroring the server-sim empty case.
		server, replicas := buildLLMDSpecs(serverName, d.Labels[ctrl.KeyServerClass],
			d.Labels[ctrl.KeyServerModel], d.Labels[ctrl.KeyAccelerator], maxQueueSize, numReplicas, nil, newLLMDMetrics())
		return server, replicas, nil
	}

	m, err := s.fetchMetrics(ctx, d.Namespace, podNames)
	if err != nil {
		return config.ServerSpec{}, nil, err
	}
	server, replicas := buildLLMDSpecs(serverName, d.Labels[ctrl.KeyServerClass],
		d.Labels[ctrl.KeyServerModel], d.Labels[ctrl.KeyAccelerator], maxQueueSize, numReplicas, podNames, m)
	return server, replicas, nil
}

// runningPodNames discovers the deployment's running pods owned by its current
// ReplicaSets (same discipline as the server-sim path).
func runningPodNames(ctx context.Context, kc kubernetes.Interface, d appsv1.Deployment) ([]string, error) {
	selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()
	rsList, err := kc.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, err
	}
	rsUIDs := make(map[types.UID]struct{})
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == d.UID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}
	pods, err := kc.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		owned := false
		for _, owner := range p.OwnerReferences {
			if _, ok := rsUIDs[owner.UID]; ok {
				owned = true
				break
			}
		}
		if owned {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// fetchMetrics runs the per-pod PromQL queries, keyed on the exact discovered
// pod names to avoid a prefix regex over-matching a sibling deployment.
func (s *llmdSensor) fetchMetrics(ctx context.Context, ns string, pods []string) (llmdMetrics, error) {
	sel := fmt.Sprintf(`namespace="%s",pod=~"%s"`, ns, strings.Join(pods, "|"))
	w := s.window
	m := newLLMDMetrics()

	tput, err := queryVector(ctx, s.api, fmt.Sprintf(`sum by(pod)(rate(vllm:request_success_total{%s}[%s]))*60`, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(&m, tput, func(pr *podReading, f float64) { pr.throughputRPM = f; pr.hasThroughput = true })

	itl, err := queryVector(ctx, s.api, fmt.Sprintf(
		`sum by(pod)(rate(vllm:request_time_per_output_token_seconds_sum{%s}[%s])) / sum by(pod)(rate(vllm:request_time_per_output_token_seconds_count{%s}[%s]))`, sel, w, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(&m, itl, func(pr *podReading, f float64) { pr.itlSec = f; pr.hasITL = true })

	ttft, err := queryVector(ctx, s.api, fmt.Sprintf(
		`sum by(pod)(rate(vllm:time_to_first_token_seconds_sum{%s}[%s])) / sum by(pod)(rate(vllm:time_to_first_token_seconds_count{%s}[%s]))`, sel, w, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(&m, ttft, func(pr *podReading, f float64) { pr.ttftSec = f; pr.hasTTFT = true })

	occ, err := queryVector(ctx, s.api, fmt.Sprintf(`avg by(pod)(avg_over_time(vllm:num_requests_running{%s}[%s]))`, sel, w))
	if err != nil {
		return m, err
	}
	collectByPod(&m, occ, func(pr *podReading, f float64) { pr.occupancy = f; pr.hasOcc = true })

	// deployment-level token averages (scalar): tokens / completions over the window.
	// An empty result (idle window at cold start) is benign and left at 0; a genuine
	// query failure is logged so a persistent zero-token condition is not silent.
	m.inTokens = s.scalarOrLog(ctx, fmt.Sprintf(
		`sum(delta(vllm:prompt_tokens_total{%s}[%s])) / sum(delta(vllm:request_success_total{%s}[%s]))`, sel, w, sel, w), "prompt-tokens")
	m.outTokens = s.scalarOrLog(ctx, fmt.Sprintf(
		`sum(delta(vllm:generation_tokens_total{%s}[%s])) / sum(delta(vllm:request_success_total{%s}[%s]))`, sel, w, sel, w), "generation-tokens")
	return m, nil
}

// scalarOrLog runs a scalar query, returning the sanitized value or 0. An empty
// result (errNoData) is silent; any other error is logged and treated as 0.
func (s *llmdSensor) scalarOrLog(ctx context.Context, query, label string) float64 {
	v, err := queryScalar(ctx, s.api, query)
	if err != nil {
		if !errors.Is(err, errNoData) {
			fmt.Printf("llmd sensor: %s query failed: %v\n", label, err)
		}
		return 0
	}
	return sanitize(v)
}

func collectByPod(m *llmdMetrics, v model.Vector, set func(*podReading, float64)) {
	for _, s := range v {
		pod := string(s.Metric["pod"])
		if pod == "" {
			continue
		}
		f := float64(s.Value)
		if f == f && !math.IsInf(f, 0) { // not NaN or ±Inf
			set(m.reading(pod), f)
		}
	}
}

func sanitize(f float64) float64 {
	if f != f || math.IsInf(f, 0) { // NaN or ±Inf
		return 0
	}
	return f
}

// buildLLMDSpecs is the pure mapping from per-pod metrics to the deployment
// ServerSpec + per-replica ServerSpecs. ITL/TTFT are converted seconds→ms and
// aggregated throughput-weighted; occupancy is the mean over reporting pods;
// ArrivalRate := Throughput.
func buildLLMDSpecs(serverName, class, model, accelerator string, maxQueueSize, numReplicas int,
	pods []string, m llmdMetrics) (config.ServerSpec, []config.ServerSpec) {

	replicas := make([]config.ServerSpec, 0, len(pods))
	// Weight ITL/TTFT only over pods that actually reported that metric, using
	// per-metric throughput-weight denominators. A pod with throughput but a
	// missing ITL/TTFT (0/0→NaN, dropped upstream) then contributes to neither
	// the numerator nor the denominator, instead of dragging the average toward 0.
	var weightedITL, itlWeight, weightedTTFT, ttftWeight, totalTputRPM, sumOcc float64
	numOcc := 0

	for _, pod := range pods {
		pr, ok := m.pods[pod]
		if !ok || !pr.hasThroughput {
			continue
		}
		tput := pr.throughputRPM
		itlMs := pr.itlSec * 1000
		ttftMs := pr.ttftSec * 1000

		replicas = append(replicas, config.ServerSpec{
			Name:         serverName + ctrl.ReplicaNameSeparator + pod,
			Class:        class,
			Model:        model,
			MaxQueueSize: maxQueueSize,
			CurrentAlloc: config.AllocationData{
				Accelerator:    accelerator,
				NumReplicas:    1,
				ITLAverage:     float32(itlMs),
				TTFTAverage:    float32(ttftMs),
				AvgConcurrency: float32(pr.occupancy),
				Load: config.ServerLoadSpec{
					ArrivalRate:  float32(tput),
					Throughput:   float32(tput),
					AvgInTokens:  int(m.inTokens),
					AvgOutTokens: int(m.outTokens),
				},
			},
		})
		totalTputRPM += tput
		if pr.hasITL {
			weightedITL += itlMs * tput
			itlWeight += tput
		}
		if pr.hasTTFT {
			weightedTTFT += ttftMs * tput
			ttftWeight += tput
		}
		if pr.hasOcc {
			sumOcc += pr.occupancy
			numOcc++
		}
	}

	var itlAvg, ttftAvg, occAvg float32
	if itlWeight > 0 {
		itlAvg = float32(weightedITL / itlWeight)
	}
	if ttftWeight > 0 {
		ttftAvg = float32(weightedTTFT / ttftWeight)
	}
	if numOcc > 0 {
		occAvg = float32(sumOcc / float64(numOcc))
	}

	server := config.ServerSpec{
		Name:         serverName,
		Class:        class,
		Model:        model,
		MaxQueueSize: maxQueueSize,
		CurrentAlloc: config.AllocationData{
			Accelerator:    accelerator,
			NumReplicas:    numReplicas,
			ITLAverage:     itlAvg,
			TTFTAverage:    ttftAvg,
			AvgConcurrency: occAvg,
			Load: config.ServerLoadSpec{
				ArrivalRate:  float32(totalTputRPM),
				Throughput:   float32(totalTputRPM),
				AvgInTokens:  int(m.inTokens),
				AvgOutTokens: int(m.outTokens),
			},
		},
	}
	return server, replicas
}
