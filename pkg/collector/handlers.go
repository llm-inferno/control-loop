package collector

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

		if d.ObjectMeta.Labels == nil || d.ObjectMeta.Labels[ctrl.KeyServerName] == "" {
			continue
		}
		serverName := d.ObjectMeta.Labels[ctrl.KeyServerName]

		depUID := string(d.UID)
		serverMap[serverName] = ctrl.ServerKubeInfo{
			UID:   depUID,
			Name:  d.ObjectMeta.Name,
			Space: d.ObjectMeta.Namespace,
		}

		numReplicas := *d.Spec.Replicas
		maxBatchSize, _ := strconv.Atoi(d.ObjectMeta.Labels[ctrl.KeyMaxBatchSize])

		var arrvRate float64 = 0.0
		var inTokens float64 = 0.0
		var outTokens float64 = 0.0

		// Query Prometheus for the arrival rate (requests/minute)
		// note: substituting missing metric arrrival rate with completion rate
		arrivalQuery := fmt.Sprintf(`sum(rate(vllm:request_success_total{job="%s"}[1m]))*60`, d.ObjectMeta.Name)
		if arrvRate, err = PrometheusQuery(arrivalQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Println("checking if label exists ...")
			arrvRate, _ = strconv.ParseFloat(d.ObjectMeta.Labels[ctrl.KeyArrivalRate], 32)
		}
		fmt.Printf("Average arrival rate %f \n", arrvRate)

		// Query Prometheus for the input token rate
		inTokenQuery := fmt.Sprintf(`delta(vllm:prompt_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`,
			d.ObjectMeta.Name, d.ObjectMeta.Name)
		if inTokens, err = PrometheusQuery(inTokenQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Printf("checking if label %s exists ...\n", ctrl.KeyInTokens)
			avgInTokensInt, _ := strconv.Atoi(d.ObjectMeta.Labels[ctrl.KeyInTokens])
			inTokens = float64(avgInTokensInt)
		}
		if math.IsNaN(inTokens) || math.IsInf(inTokens, 0) {
			inTokens = 0.0
		}
		fmt.Printf("Average input tokens per request %f \n", inTokens)

		// Query Prometheus for the output token rate
		outTokenQuery := fmt.Sprintf(`delta(vllm:generation_tokens_total{job="%s"}[1m])/delta(vllm:request_success_total{job="%s"}[1m])`,
			d.ObjectMeta.Name, d.ObjectMeta.Name)
		if outTokens, err = PrometheusQuery(outTokenQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Printf("checking if label %s exists ...\n", ctrl.KeyOutTokens)
			avgOutTokensInt, _ := strconv.Atoi(d.ObjectMeta.Labels[ctrl.KeyOutTokens])
			outTokens = float64(avgOutTokensInt)
		}
		if math.IsNaN(outTokens) || math.IsInf(outTokens, 0) {
			outTokens = 0.0
		}
		fmt.Printf("Average output tokens per request %f \n", outTokens)

		curAlloc := config.AllocationData{
			Accelerator: d.ObjectMeta.Labels[ctrl.KeyAccelerator],
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
			Class:        d.ObjectMeta.Labels[ctrl.KeyServerClass],
			Model:        d.ObjectMeta.Labels[ctrl.KeyServerModel],
			CurrentAlloc: curAlloc,
		}
		serverSpecs = append(serverSpecs, serverSpec)
	}

	serverCollectorInfo := ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		KubeResource: serverMap,
	}

	c.IndentedJSON(http.StatusOK, serverCollectorInfo)
}
