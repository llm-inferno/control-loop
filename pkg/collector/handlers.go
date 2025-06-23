package collector

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/inferno/pkg/config"

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
		var avglength float64 = 0.0

		// Query Prometheus for the arrival rate (requests/minute)
		arrivalQuery := fmt.Sprintf(`sum(rate(vllm:requests_count_total{job="%s"}[1m]))*60`, d.ObjectMeta.Name)
		if arrvRate, err = PrometheusQuery(arrivalQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Println("checking if label exists ...")
			arrvRate, _ = strconv.ParseFloat(d.ObjectMeta.Labels[ctrl.KeyArrivalRate], 32)
		}
		fmt.Printf("Average arrival rate %f \n", arrvRate)

		// Query Prometheus for the token rate
		tokenQuery := fmt.Sprintf(`delta(vllm:tokens_count_total{job="%s"}[1m])/delta(vllm:requests_count_total{job="%s"}[1m])`,
			d.ObjectMeta.Name, d.ObjectMeta.Name)
		if avglength, err = PrometheusQuery(tokenQuery); err != nil {
			fmt.Println(err.Error())
			// check if label exists as a backup
			fmt.Println("checking if label exists ...")
			avglengthInt, _ := strconv.Atoi(d.ObjectMeta.Labels[ctrl.KeyNumTokens])
			avglength = float64(avglengthInt)
		}
		if math.IsNaN(avglength) || math.IsInf(avglength, 0) {
			avglength = 0.0
		}
		fmt.Printf("Average token length per request %f \n", avglength)

		curAlloc := config.AllocationData{
			Accelerator: d.ObjectMeta.Labels[ctrl.KeyAccelerator],
			NumReplicas: int(numReplicas),
			MaxBatch:    maxBatchSize,
			Load: config.ServerLoadSpec{
				ArrivalRate: float32(arrvRate),
				AvgLength:   int(avglength),
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
