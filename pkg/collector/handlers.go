package collector

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"

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

		numReplicas := *d.Spec.Replicas
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

		curAlloc := config.AllocationData{
			Accelerator: d.Labels[ctrl.KeyAccelerator],
			NumReplicas: int(numReplicas),
			MaxBatch:    maxBatchSize,
			Load: config.ServerLoadSpec{
				ArrivalRate:  float32(arrvRate),
				AvgInTokens:  int(inTokens),
				AvgOutTokens: int(outTokens),
			},
		}

		serverSpec := config.ServerSpec{
			Name:         serverName,
			Class:        d.Labels[ctrl.KeyServerClass],
			Model:        d.Labels[ctrl.KeyServerModel],
			CurrentAlloc: curAlloc,
		}
		serverSpecs = append(serverSpecs, serverSpec)

		// simulate each running pod via server-sim sidecar
		selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()

		// find ReplicaSets owned by this deployment
		rsList, err := KubeClient.AppsV1().ReplicaSets(d.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: selectorStr})
		if err != nil {
			fmt.Printf("error listing ReplicaSets for %s: %v\n", serverName, err)
			continue
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

		// find running pods owned by those ReplicaSets
		pods, err := KubeClient.CoreV1().Pods(d.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: selectorStr})
		if err != nil {
			fmt.Printf("error listing pods for %s: %v\n", serverName, err)
			continue
		}
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

			req := simRequest{
				RPS:             float32(rpm / 60.0),
				MaxConcurrency:  maxBatchSize,
				AvgInputTokens:  float32(inTok),
				AvgOutputTokens: float32(outTok),
				Accelerator:     d.Labels[ctrl.KeyAccelerator],
				Model:           d.Labels[ctrl.KeyServerModel],
			}
			result, err := simulatePod(KubeClient, p.Namespace, p.Name, ctrl.ServerSimPort, req)
			if err != nil {
				fmt.Printf("pod %s simulation error: %v\n", p.Name, err)
				continue
			}
			fmt.Printf("pod %s: TTFT=%.1fms ITL=%.1fms throughput=%.2freq/s maxRPS=%.2f\n",
				p.Name, result.AvgTTFT, result.AvgITL, result.Throughput, result.MaxRPS)
		}
	}

	serverCollectorInfo := ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		KubeResource: serverMap,
	}

	c.IndentedJSON(http.StatusOK, serverCollectorInfo)
}
