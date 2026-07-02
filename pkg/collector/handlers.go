package collector

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sensor is selected once at package init from INFERNO_BACKEND.
var sensor backend.Sensor = selectSensor()

func selectSensor() backend.Sensor {
	if backend.ModeFromEnv() == backend.ModeLLMD {
		fmt.Println("collector: using llmd sensor")
		return newLLMDSensor()
	}
	fmt.Println("collector: using serversim sensor")
	return newServerSimSensor()
}

func collect(c *gin.Context) {
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments(ctrl.WatchNamespace()).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
	if err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "kube client: " + err.Error()})
		return
	}

	serverSpecs := make([]config.ServerSpec, 0)
	replicaSpecs := make([]config.ServerSpec, 0)
	serverMap := make(map[string]ctrl.ServerKubeInfo)

	for _, d := range deps.Items {
		if d.Labels == nil || d.Labels[ctrl.KeyServerName] == "" {
			continue
		}
		serverName := d.Labels[ctrl.KeyServerName]
		serverMap[serverName] = ctrl.ServerKubeInfo{
			UID:   string(d.UID),
			Name:  d.Name,
			Space: d.Namespace,
		}

		spec, replicas, err := sensor.Sense(context.TODO(), d, KubeClient)
		if err != nil {
			fmt.Printf("sense %s: %v\n", serverName, err)
			// Known limitation: with multiple managed variants, a transient sense error
			// would drop this deployment from Spec, but the actuator could still scale
			// it to zero. Deferred — multi-variant llmd is an explicit non-goal this phase.
			// (Single variant is safe: an empty Spec aborts the whole cycle at
			// controller.go's len(Spec)==0 guard before any actuation.)
			continue
		}
		serverSpecs = append(serverSpecs, spec)
		replicaSpecs = append(replicaSpecs, replicas...)
	}

	for _, r := range replicaSpecs {
		fmt.Printf("replicaAlloc[%s]: acc=%s maxBatch=%d ITL=%.1fms TTFT=%.1fms arrivalRateRPM=%.2f throughputRPM=%.2f inTok=%d outTok=%d occ=%.2f\n",
			r.Name, r.CurrentAlloc.Accelerator, r.CurrentAlloc.MaxBatch,
			r.CurrentAlloc.ITLAverage, r.CurrentAlloc.TTFTAverage,
			r.CurrentAlloc.Load.ArrivalRate, r.CurrentAlloc.Load.Throughput, r.CurrentAlloc.Load.AvgInTokens, r.CurrentAlloc.Load.AvgOutTokens,
			r.CurrentAlloc.AvgConcurrency)
	}

	c.IndentedJSON(http.StatusOK, ctrl.ServerCollectorInfo{
		Spec:         serverSpecs,
		ReplicaSpecs: replicaSpecs,
		KubeResource: serverMap,
	})
}
