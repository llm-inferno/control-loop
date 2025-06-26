package actuator

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/optimizer/pkg/config"

	"github.com/gin-gonic/gin"
	v1 "k8s.io/api/apps/v1"
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
	allocMap := info.Spec
	serverMap := info.KubeResource

	// get managed deployments
	labelSelector := ctrl.KeyManaged + "=true"
	deps, err := KubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector})
	if err != nil {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "kube client: " + err.Error()})
		return
	}

	updatedDeployments := map[string]bool{}

	//update deployments
	for serverName, allocData := range allocMap {

		var dep ctrl.ServerKubeInfo
		var exists bool
		if dep, exists = serverMap[serverName]; !exists {
			continue
		}

		deployUID := dep.UID
		deployName := dep.Name
		nameSpace := dep.Space

		// TODO: need more efficient search
		// find deployment by name
		for _, d := range deps.Items {
			if string(d.UID) == deployUID {
				if err := patchDeployment(d, serverName, deployName, nameSpace, &allocData); err != nil {
					c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "kube client: " + err.Error()})
					return
				}
				updatedDeployments[deployUID] = true
			}
		}
	}

	for _, d := range deps.Items {
		deployUID := string(d.UID)
		if updatedDeployments[deployUID] {
			continue
		}

		serverName := d.Labels[ctrl.KeyServerName]
		deployName := d.Name
		nameSpace := d.Namespace

		// set allocation to none (no feasible allocation was found by the optimizer)
		allocData := &config.AllocationData{
			Accelerator: "",
			NumReplicas: 0,
			MaxBatch:    0,
			Load: config.ServerLoadSpec{
				ArrivalRate: 0,
				AvgLength:   0,
			},
		}

		if err := patchDeployment(d, serverName, deployName, nameSpace, allocData); err != nil {
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "kube client: " + err.Error()})
			return
		}
	}

	c.IndentedJSON(http.StatusOK, "Done")
}

func patchDeployment(d v1.Deployment, serverName, deployName, nameSpace string, allocData *config.AllocationData) error {
	acceleratorName := allocData.Accelerator
	numReplicas := int32(allocData.NumReplicas)
	maxBatchSize := allocData.MaxBatch

	// patch numReplicas and labels
	patchAcc := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%s"}`, ctrl.KeyAccelerator, acceleratorName)
	patchBatch := fmt.Sprintf(`{"op": "replace", "path": "/metadata/labels/%s", "value": "%d"}`, ctrl.KeyMaxBatchSize, maxBatchSize)
	patchRep := fmt.Sprintf(`{"op": "replace", "path": "/spec/replicas", "value": %d}`, numReplicas)
	patchAll := []byte(`[` + patchAcc + `,` + patchBatch + `,` + patchRep + `]`)

	// TODO: fix this
	// print change - for testing
	curMaxBatchSize, _ := strconv.Atoi(d.Labels[ctrl.KeyMaxBatchSize])
	curRPM := allocData.Load.ArrivalRate
	curNumTokens := allocData.Load.AvgLength
	fmt.Printf("srv=[%s/%s/%s]: rpm=%.2f; tok=%d; acc=%s->%s; num=%d->%d; batch=%d->%d \n",
		serverName, d.Labels[ctrl.KeyServerClass], d.Labels[ctrl.KeyServerModel],
		curRPM, curNumTokens,
		d.Labels[ctrl.KeyAccelerator], acceleratorName,
		*d.Spec.Replicas, numReplicas, curMaxBatchSize, maxBatchSize)

	// update deployment
	if _, err := KubeClient.AppsV1().Deployments(nameSpace).Patch(context.Background(), deployName,
		types.JSONPatchType, patchAll, metav1.PatchOptions{}); err != nil {
		return err
	}
	return nil
}
