package collector

import (
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
)

const (
	// overloadTargetUtilization is the initial fraction of MaxRPS for the first re-simulation
	// attempt when a pod reports saturation, targeting a stable ~90% utilization operating point.
	overloadTargetUtilization = float32(0.90)

	// overloadRetryStep is the utilization reduction applied on each successive re-simulation
	// attempt (0.90 → 0.75 → 0.60 of MaxRPS).
	overloadRetryStep = float32(0.15)

	// overloadMaxRetries is the maximum number of re-simulation attempts before the pod is
	// skipped entirely when saturation persists.
	overloadMaxRetries = 3
)

// Kube client as global variable, used by handler functions
var KubeClient *kubernetes.Clientset

// Collector REST server
type Collector struct {
	router *gin.Engine
}

// create a new Collector
func NewCollector() (collector *Collector, err error) {
	if KubeClient, err = ctrl.GetKubeClient(); err != nil {
		return nil, err
	}
	collector = &Collector{
		router: gin.Default(),
	}
	collector.router.GET("/collect", collect)
	return collector, nil
}

// start server
func (server *Collector) Run(host, port string) {
	_ = server.router.Run(host + ":" + port)
}
