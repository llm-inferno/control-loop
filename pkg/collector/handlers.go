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
	// get managed deployments
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
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

		var arrvRate float64
		var inTokens float64
		var outTokens float64

		// Query Prometheus for the arrival rate (requests/minute)
		// note: substituting missing metric arrrival rate with completion rate
		arrivalQuery := fmt.Sprintf(`sum(rate(vllm:request_success_total{job="%s"}[1m]))*60`, d.Name)
		if arrvRate, err = PrometheusQuery(arrivalQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Println("checking if label exists ...")
			arrvRate, _ = strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 32)
		}
		fmt.Printf("Average arrival rate %f \n", arrvRate)

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
				// collect running pods owned by this deployment
				type podEntry struct {
					pod    corev1.Pod
					rpm    float64
					inTok  int
					outTok int
				}
				var runningPods []podEntry
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
					rpm, _ := strconv.ParseFloat(p.Labels[ctrl.KeyArrivalRate], 32)
					inTok, _ := strconv.Atoi(p.Labels[ctrl.KeyInTokens])
					outTok, _ := strconv.Atoi(p.Labels[ctrl.KeyOutTokens])
					runningPods = append(runningPods, podEntry{p, rpm, inTok, outTok})
				}
				numReplicas = len(runningPods)

				// fan-out: simulate all pods in parallel
				simResults := make([]*simResult, len(runningPods))
				simErrors := make([]error, len(runningPods))
				var wg sync.WaitGroup
				for i, pe := range runningPods {
					wg.Add(1)
					go func(i int, pe podEntry) {
						defer wg.Done()
						req := simRequest{
							RPS:             float32(pe.rpm / 60.0),
							MaxConcurrency:  maxBatchSize,
							AvgInputTokens:  float32(pe.inTok),
							AvgOutputTokens: float32(pe.outTok),
							Accelerator:     d.Labels[ctrl.KeyAccelerator],
							Model:           d.Labels[ctrl.KeyServerModel],
						}
						simResults[i], simErrors[i] = simulatePod(KubeClient, pe.pod.Namespace, pe.pod.Name, ctrl.ServerSimPort, req)
					}(i, pe)
				}
				wg.Wait()

				// aggregate results
				var weightedITL, weightedTTFT, totalRPM float64
				for i, pe := range runningPods {
					if simErrors[i] != nil {
						fmt.Printf("pod %s simulation error: %v\n", pe.pod.Name, simErrors[i])
						continue
					}
					podITL := simResults[i].AvgITL
					podTTFT := simResults[i].AvgTTFT
					podThroughputRPM := float64(simResults[i].Throughput) * 60.0
					fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughput=%.2freq/s maxRPS=%.2f\n",
						pe.pod.Name, podTTFT, podITL, simResults[i].Throughput, simResults[i].MaxRPS)
					weightedITL += float64(podITL) * podThroughputRPM
					weightedTTFT += float64(podTTFT) * podThroughputRPM
					totalRPM += podThroughputRPM
					replicaSpecs = append(replicaSpecs, config.ServerSpec{
						Name:  serverName + ctrl.ReplicaNameSeparator + pe.pod.Name,
						Class: d.Labels[ctrl.KeyServerClass],
						Model: d.Labels[ctrl.KeyServerModel],
						CurrentAlloc: config.AllocationData{
							Accelerator: d.Labels[ctrl.KeyAccelerator],
							MaxBatch:    maxBatchSize,
							NumReplicas: 1,
							ITLAverage:  podITL,
							TTFTAverage: podTTFT,
							Load: config.ServerLoadSpec{
								ArrivalRate:  float32(podThroughputRPM),
								AvgInTokens:  pe.inTok,
								AvgOutTokens: pe.outTok,
							},
						},
					})
				}
				if totalRPM > 0 {
					itlAvg = float32(weightedITL / totalRPM)
					ttftAvg = float32(weightedTTFT / totalRPM)
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
				ArrivalRate:  float32(arrvRate),
				AvgInTokens:  int(inTokens),
				AvgOutTokens: int(outTokens),
			},
		}

		fmt.Printf("curAlloc[%s]: replicas=%d acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms rpm=%.2f inTok=%d outTok=%d\n",
			serverName, curAlloc.NumReplicas, curAlloc.Accelerator, curAlloc.MaxBatch,
			curAlloc.ITLAverage, curAlloc.TTFTAverage,
			curAlloc.Load.ArrivalRate, curAlloc.Load.AvgInTokens, curAlloc.Load.AvgOutTokens)

		serverSpec := config.ServerSpec{
			Name:         serverName,
			Class:        d.Labels[ctrl.KeyServerClass],
			Model:        d.Labels[ctrl.KeyServerModel],
			CurrentAlloc: curAlloc,
		}
		serverSpecs = append(serverSpecs, serverSpec)
	}

	for _, r := range replicaSpecs {
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms rpm=%.2f inTok=%d outTok=%d\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens)
	}

	serverCollectorInfo := ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		ReplicaSpecs: replicaSpecs,
		KubeResource: serverMap,
	}

	c.IndentedJSON(http.StatusOK, serverCollectorInfo)
}
