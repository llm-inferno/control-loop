package collector

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// Handlers for REST API calls

func collect(c *gin.Context) {
	// get managed deployments (scoped to WATCH_NAMESPACE if set; empty means cluster-wide)
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
	if err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "kube client: " + err.Error()})
		return
	}

	// initialize collector info
	serverSpecs := make([]config.ServerSpec, 0)
	replicaSpecs := make([]config.ServerSpec, 0)
	serverMap := make(map[string]ctrl.ServerKubeInfo)

	// collect data from deployments
	for _, d := range deps.Items {

		if d.Labels == nil || d.Labels[ctrl.KeyServerName] == "" {
			continue
		}
		serverName := d.Labels[ctrl.KeyServerName]

		depUID := string(d.UID)
		serverMap[serverName] = ctrl.ServerKubeInfo{
			UID:   depUID,
			Name:  d.Name,
			Space: d.Namespace,
		}

		maxBatchSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxBatchSize])
		maxQueueSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxQueueSize])

		var arrvRate float64
		var inTokens float64
		var outTokens float64

		// Query Prometheus for the throughput (completed requests/minute).
		// TODO: use a separate arrival-rate query (e.g. vllm:request_arrival_total) when
		// that metric becomes available; for now arrival rate and throughput use the same query.
		throughputQuery := fmt.Sprintf(`sum(rate(vllm:request_success_total{job="%s"}[1m]))*60`, d.Name)
		if arrvRate, err = PrometheusQuery(throughputQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Println("checking if label exists ...")
			arrvRate, _ = strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 32)
		}
		fmt.Printf("Average arrival rate / throughput %f \n", arrvRate)

		// Query Prometheus for the input token rate
		inTokenQuery := fmt.Sprintf(`delta(vllm:prompt_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`,
			d.Name, d.Name)
		if inTokens, err = PrometheusQuery(inTokenQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Printf("checking if label %s exists ...\n", ctrl.KeyInTokens)
			avgInTokensInt, _ := strconv.Atoi(d.Labels[ctrl.KeyInTokens])
			inTokens = float64(avgInTokensInt)
		}
		if math.IsNaN(inTokens) || math.IsInf(inTokens, 0) {
			inTokens = 0.0
		}
		fmt.Printf("Average input tokens per request %f \n", inTokens)

		// Query Prometheus for the output token rate
		outTokenQuery := fmt.Sprintf(`delta(vllm:generation_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`,
			d.Name, d.Name)
		if outTokens, err = PrometheusQuery(outTokenQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Printf("checking if label %s exists ...\n", ctrl.KeyOutTokens)
			avgOutTokensInt, _ := strconv.Atoi(d.Labels[ctrl.KeyOutTokens])
			outTokens = float64(avgOutTokensInt)
		}
		if math.IsNaN(outTokens) || math.IsInf(outTokens, 0) {
			outTokens = 0.0
		}
		fmt.Printf("Average output tokens per request %f \n", outTokens)

		// simulate running pods and compute weighted average ITL/TTFT
		var itlAvg, ttftAvg float32
		var totalThroughputRPM float64
		var numReplicas int
		selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()

		rsList, err := KubeClient.AppsV1().ReplicaSets(d.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: selectorStr})
		if err != nil {
			fmt.Printf("error listing ReplicaSets for %s: %v\n", serverName, err)
		} else {
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
				fmt.Printf("error listing pods for %s: %v\n", serverName, err)
			} else {
				// collect running, ready pods owned by this deployment
				var runningPods []corev1.Pod
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
					if !owned {
						continue
					}
					if !ctrl.IsPodReady(p.Status.StartTime) {
						fmt.Printf("pod %s: skipping (within startup delay)\n", p.Name)
						continue
					}
					runningPods = append(runningPods, p)
				}
				numReplicas = int(*d.Spec.Replicas)

				// fan-out: read each pod's latest completed result (non-blocking)
				envs := make([]*latestEnvelope, len(runningPods))
				errs := make([]error, len(runningPods))
				var wg sync.WaitGroup
				for i, p := range runningPods {
					wg.Add(1)
					go func(i int, p corev1.Pod) {
						defer wg.Done()
						envs[i], errs[i] = getLatest(KubeClient, p.Namespace, p.Name, ctrl.ServerSimPort)
					}(i, p)
				}
				wg.Wait()

				// aggregate
				var weightedITL, weightedTTFT float64
				for i, p := range runningPods {
					if errs[i] != nil {
						fmt.Printf("pod %s: no result this cycle (%v); skipping\n", p.Name, errs[i])
						continue
					}
					spec, ok := buildReplicaSpec(serverName, p.Name,
						d.Labels[ctrl.KeyServerClass], d.Labels[ctrl.KeyServerModel],
						maxQueueSize, maxBatchSize, d.Labels[ctrl.KeyAccelerator], envs[i])
					if !ok {
						if envs[i] == nil {
							fmt.Printf("pod %s: no usable result this cycle; holding\n", p.Name)
						} else {
							fmt.Printf("pod %s: stale result (effectiveConcurrency=%d != inForce=%d); holding\n",
								p.Name, envs[i].EffectiveInput.MaxConcurrency, maxBatchSize)
						}
						continue
					}
					w := float64(spec.CurrentAlloc.Load.Throughput)
					fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughputRPM=%.2f\n",
						p.Name, spec.CurrentAlloc.TTFTAverage, spec.CurrentAlloc.ITLAverage, w)
					weightedITL += float64(spec.CurrentAlloc.ITLAverage) * w
					weightedTTFT += float64(spec.CurrentAlloc.TTFTAverage) * w
					totalThroughputRPM += w
					replicaSpecs = append(replicaSpecs, spec)
				}
				if totalThroughputRPM > 0 {
					itlAvg = float32(weightedITL / totalThroughputRPM)
					ttftAvg = float32(weightedTTFT / totalThroughputRPM)
				}
			}
		}

		curAlloc := config.AllocationData{
			Accelerator: d.Labels[ctrl.KeyAccelerator],
			NumReplicas: int(numReplicas),
			MaxBatch:    maxBatchSize,
			ITLAverage:  itlAvg,
			TTFTAverage: ttftAvg,
			Load: config.ServerLoadSpec{
				// TODO: use a separate arrival-rate query when available;
				// for now arrival rate and throughput are both set from the same Prometheus query.
				ArrivalRate:  float32(arrvRate),
				Throughput:   float32(totalThroughputRPM),
				AvgInTokens:  int(inTokens),
				AvgOutTokens: int(outTokens),
			},
		}

		fmt.Printf("curAlloc[%s]: replicas=%d acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d\n",
			serverName, curAlloc.NumReplicas, curAlloc.Accelerator, curAlloc.MaxBatch,
			curAlloc.ITLAverage, curAlloc.TTFTAverage,
			curAlloc.Load.ArrivalRate, curAlloc.Load.Throughput, curAlloc.Load.AvgInTokens, curAlloc.Load.AvgOutTokens)

		serverSpec := config.ServerSpec{
			Name:         serverName,
			Class:        d.Labels[ctrl.KeyServerClass],
			Model:        d.Labels[ctrl.KeyServerModel],
			MaxQueueSize: maxQueueSize,
			CurrentAlloc: curAlloc,
		}
		serverSpecs = append(serverSpecs, serverSpec)
	}

	for _, r := range replicaSpecs {
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.Throughput, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens)
	}

	serverCollectorInfo := ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		ReplicaSpecs: replicaSpecs,
		KubeResource: serverMap,
	}

	c.IndentedJSON(http.StatusOK, serverCollectorInfo)
}
