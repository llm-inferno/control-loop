package actuator

import (
	"context"
	"fmt"
	"net/http"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Handlers for REST API calls

func update(c *gin.Context) {
	var info ctrl.ServerActuatorInfo
	if err := c.BindJSON(&info); err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "binding error: " + err.Error()})
		return
	}

	// Drive updates from the Collector-built serverMap. The set zeroed out is
	// {serverMap - allocMap}: any managed deployment the Collector saw for
	// which the Optimizer did not return an allocation gets replicas=0.
	//
	// The Actuator does not re-verify that targets carry the
	// `inferno.server.managed=true` label; the Collector enforces that
	// invariant when it builds serverMap. This handler trusts its caller —
	// consistent with the rest of the in-pod control plane, which is bound
	// to localhost and has no auth middleware. Do not derive patch targets
	// from any other source without restoring a server-side label gate.
	updates := ComputeUpdates(info.Spec, info.KubeResource)

	for _, u := range updates {
		if err := patchDeployment(u.ServerName, u.DeployName, u.Namespace, &u.Allocation); err != nil {
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
