package collector

import (
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
)

const (
	// overloadSaturationThreshold is the ratio of Throughput/MaxRPS above which a pod is
	// considered near saturation and its simulation results are not useful for EKF tuning.
	overloadSaturationThreshold = float32(0.95)

	// overloadTargetUtilization is the fraction of MaxRPS used for the re-simulation when
	// a pod is detected as overloaded, targeting a stable ~90% utilization operating point.
	overloadTargetUtilization = float32(0.90)
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
