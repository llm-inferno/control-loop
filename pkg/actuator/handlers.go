package actuator

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// actuatorImpl is selected once from INFERNO_BACKEND.
var actuatorImpl backend.Actuator = selectActuator()

func selectActuator() backend.Actuator {
	if backend.ModeFromEnv() == backend.ModeLLMD {
		fmt.Println("actuator: using llmd actuator (replicas only)")
		return newLLMDActuator()
	}
	fmt.Println("actuator: using serversim actuator")
	return newServerSimActuator()
}

func update(c *gin.Context) {
	var info ctrl.ServerActuatorInfo
	if err := c.BindJSON(&info); err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "binding error: " + err.Error()})
		return
	}
	updates := ComputeUpdates(info.Spec, info.KubeResource)
	for _, u := range updates {
		if err := actuatorImpl.Actuate(context.Background(), KubeClient, u); err != nil {
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "kube client: " + err.Error()})
			return
		}
	}
	c.IndentedJSON(http.StatusOK, "Done")
}

// patchDeployment applies the optimizer's allocation (or the zero allocation
// for "no feasible solution") to a single managed Deployment. The Deployment
// is identified by name + namespace; no full v1.Deployment lookup is needed.
func patchDeployment(serverName, deployName, nameSpace string, allocData *config.AllocationData) error {
	acceleratorName := allocData.Accelerator
	numReplicas := int32(allocData.NumReplicas)
	maxBatchSize := allocData.MaxBatch

	patchAcc := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%s"}`, ctrl.KeyAccelerator, acceleratorName)
	patchBatch := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%d"}`, ctrl.KeyMaxBatchSize, maxBatchSize)
	patchRep := fmt.Sprintf(`{"op": "replace", "path": "/spec/replicas", "value": %d}`, numReplicas)
	patchAll := []byte(`[` + patchAcc + `,` + patchBatch + `,` + patchRep + `]`)

	arrivalRateRPM := allocData.Load.ArrivalRate
	curInTokens := allocData.Load.AvgInTokens
	curOutTokens := allocData.Load.AvgOutTokens
	fmt.Printf("srv=[%s/%s]: arrivalRateRPM=%.2f; inTok=%d; outTok=%d; acc=%s; num=%d; batch=%d \n",
		serverName, nameSpace,
		arrivalRateRPM, curInTokens, curOutTokens,
		acceleratorName, numReplicas, maxBatchSize)

	if _, err := KubeClient.AppsV1().Deployments(nameSpace).Patch(context.Background(), deployName,
		types.JSONPatchType, patchAll, metav1.PatchOptions{}); err != nil {
		return err
	}
	return nil
}
