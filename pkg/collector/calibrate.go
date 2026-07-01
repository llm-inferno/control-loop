package collector

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// sweepPoint is one operating point of the benchmarking-on-the-fly sweep grid.
type sweepPoint struct {
	rpm     float64 // offered load (requests/minute)
	inToks  float64 // average input tokens per request
	outToks float64 // average output tokens per request
}

// sweep runs a short load sweep against one managed server and returns the measured operating
// points as []config.ServerSpec, ready to POST to the tuner's /calibrate. It is the collector
// half of benchmarking-on-the-fly: only the collector has the kube client and can drive a pod's
// server-sim /simulate. The sweep deliberately spans a range of arrival rates and two token-mix
// ratios so a joint fit can identify alpha/beta/gamma (persistent excitation) — something a single
// in-force operating point cannot. Saturated and invalid points are dropped (they are not clean
// calibration anchors). GET /sweep?server=<name>
func sweep(c *gin.Context) {
	server := c.Query("server")
	if server == "" {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "missing required query parameter: server"})
		return
	}

	pod, depLabels, err := findServerPod(server)
	if err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	model := depLabels[ctrl.KeyServerModel]
	accelerator := depLabels[ctrl.KeyAccelerator]
	maxQueueSize, _ := strconv.Atoi(depLabels[ctrl.KeyMaxQueueSize])
	concurrency, _ := strconv.Atoi(depLabels[ctrl.KeyMaxBatchSize])
	if concurrency <= 0 {
		c.IndentedJSON(http.StatusUnprocessableEntity, gin.H{
			"message": fmt.Sprintf("server %s has no in-force maxbatchsize; cannot calibrate", server)})
		return
	}

	baseRPM := labelFloat(depLabels, ctrl.KeyNominalArrivalRate, ctrl.KeyArrivalRate)
	baseIn := labelFloat(depLabels, ctrl.KeyNominalInTokens, ctrl.KeyInTokens)
	baseOut := labelFloat(depLabels, ctrl.KeyNominalOutTokens, ctrl.KeyOutTokens)
	if baseRPM <= 0 || baseIn <= 0 || baseOut <= 0 {
		c.IndentedJSON(http.StatusUnprocessableEntity, gin.H{
			"message": fmt.Sprintf("server %s missing nominal load labels (rpm=%.1f in=%.1f out=%.1f)",
				server, baseRPM, baseIn, baseOut)})
		return
	}

	points := buildSweepGrid(baseRPM, baseIn, baseOut)
	timeout := calibPointTimeout()
	pollInterval := calibPollInterval()

	specs := make([]config.ServerSpec, 0, len(points))
	for _, pt := range points {
		req := simRequest{
			RPS:             float32(pt.rpm / 60.0),
			MaxConcurrency:  concurrency,
			AvgInputTokens:  float32(pt.inToks),
			AvgOutputTokens: float32(pt.outToks),
			Accelerator:     accelerator,
			Model:           model,
		}
		res, err := runSimulate(KubeClient, pod.Namespace, pod.Name, ctrl.ServerSimPort, req, timeout, pollInterval)
		if err != nil {
			fmt.Printf("sweep[%s]: point rpm=%.1f in=%.0f out=%.0f failed: %v; skipping\n",
				server, pt.rpm, pt.inToks, pt.outToks, err)
			continue
		}
		if res.Saturation != "" {
			fmt.Printf("sweep[%s]: point rpm=%.1f saturated (%s); skipping\n", server, pt.rpm, res.Saturation)
			continue
		}
		if res.AvgTTFT <= 0 || res.AvgITL <= 0 {
			fmt.Printf("sweep[%s]: point rpm=%.1f no usable latency (TTFT=%.1f ITL=%.1f); skipping\n",
				server, pt.rpm, res.AvgTTFT, res.AvgITL)
			continue
		}
		fmt.Printf("sweep[%s]: rpm=%.1f in=%.0f out=%.0f -> TTFT=%.1fms ITL=%.1fms thrpt=%.2f\n",
			server, pt.rpm, pt.inToks, pt.outToks, res.AvgTTFT, res.AvgITL, res.Throughput)
		specs = append(specs, config.ServerSpec{
			Name:         server,
			Model:        model,
			MaxQueueSize: maxQueueSize,
			MaxBatchSize: concurrency,
			CurrentAlloc: config.AllocationData{
				Accelerator: accelerator,
				MaxBatch:    concurrency,
				ITLAverage:  res.AvgITL,
				TTFTAverage: res.AvgTTFT,
				Load: config.ServerLoadSpec{
					ArrivalRate:  float32(pt.rpm),
					Throughput:   res.Throughput * 60.0,
					AvgInTokens:  int(pt.inToks),
					AvgOutTokens: int(pt.outToks),
				},
			},
		})
	}

	fmt.Printf("sweep[%s]: collected %d/%d usable calibration points\n", server, len(specs), len(points))
	c.IndentedJSON(http.StatusOK, specs)
}

// findServerPod locates a running, ready pod backing the named managed server and returns it
// along with the deployment labels (load/allocation metadata for the sweep grid). Mirrors the
// deployment→ReplicaSet→pod ownership resolution in collect().
func findServerPod(server string) (corev1.Pod, map[string]string, error) {
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
	if err != nil {
		return corev1.Pod{}, nil, fmt.Errorf("list managed deployments: %w", err)
	}

	for i := range deps.Items {
		d := deps.Items[i]
		if d.Labels == nil || d.Labels[ctrl.KeyServerName] != server {
			continue
		}

		selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()
		rsList, err := KubeClient.AppsV1().ReplicaSets(d.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: selectorStr})
		if err != nil {
			return corev1.Pod{}, nil, fmt.Errorf("list replicasets for %s: %w", server, err)
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

		pods, err := KubeClient.CoreV1().Pods(d.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: selectorStr})
		if err != nil {
			return corev1.Pod{}, nil, fmt.Errorf("list pods for %s: %w", server, err)
		}
		for j := range pods.Items {
			p := pods.Items[j]
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
			if !owned || !ctrl.IsPodReady(p.Status.StartTime) {
				continue
			}
			return p, d.Labels, nil
		}
		return corev1.Pod{}, nil, fmt.Errorf("no running ready pod for server %s", server)
	}
	return corev1.Pod{}, nil, fmt.Errorf("managed server %s not found", server)
}

// buildSweepGrid builds the calibration operating points: an arrival-rate ramp at the base token
// mix (moves queueing/batch occupancy, identifies alpha), plus two points with skewed input/output
// token ratios (separates the prefill/compute term beta from the decode/memory term gamma, which a
// fixed token mix cannot). The token-ratio points are pinned to the lowest ramp rate — not the
// base rate — and use a wide 4x ratio swing, so they stay unsaturated and survive (the base rate
// on a high-nominal workload is already near the knee, where a token-heavy point saturates and is
// dropped, which is what starved beta/gamma separation before). Saturated points are dropped
// downstream, so over-reaching the ramp top is harmless.
func buildSweepGrid(baseRPM, baseIn, baseOut float64) []sweepPoint {
	factors := calibRPMFactors()
	points := make([]sweepPoint, 0, len(factors)+2)
	for _, f := range factors {
		points = append(points, sweepPoint{rpm: baseRPM * f, inToks: baseIn, outToks: baseOut})
	}
	lowest := factors[0]
	for _, f := range factors {
		if f < lowest {
			lowest = f
		}
	}
	anchorRPM := baseRPM * lowest
	points = append(points,
		sweepPoint{rpm: anchorRPM, inToks: baseIn * 2.0, outToks: baseOut * 0.5}, // input-heavy: leverage on beta
		sweepPoint{rpm: anchorRPM, inToks: baseIn * 0.5, outToks: baseOut * 2.0}, // output-heavy: leverage on gamma
	)
	return points
}

// labelFloat reads a float label, preferring primary (e.g. nominal) then falling back to secondary
// (e.g. the current value); returns 0 if neither parses.
func labelFloat(lbls map[string]string, primary, secondary string) float64 {
	for _, k := range []string{primary, secondary} {
		if v, err := strconv.ParseFloat(lbls[k], 64); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// calibRPMFactors returns the arrival-rate multipliers (of nominal RPM) for the sweep ramp.
func calibRPMFactors() []float64 {
	raw := os.Getenv(ctrl.CalibRPMFactorsEnvName)
	if raw == "" {
		raw = ctrl.DefaultCalibRPMFactors
	}
	var factors []float64
	for _, s := range strings.Split(raw, ",") {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 {
			factors = append(factors, f)
		}
	}
	if len(factors) == 0 {
		factors = []float64{0.5, 1.0, 2.0} // safety net if the env is set to garbage
	}
	return factors
}

func calibPointTimeout() time.Duration {
	secs := ctrl.DefaultCalibPointTimeoutSec
	if v := os.Getenv(ctrl.CalibPointTimeoutSecEnvName); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	return time.Duration(secs) * time.Second
}

func calibPollInterval() time.Duration {
	secs := ctrl.DefaultCalibPollIntervalSec
	if v := os.Getenv(ctrl.CalibPollIntervalSecEnvName); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	return time.Duration(secs) * time.Second
}
