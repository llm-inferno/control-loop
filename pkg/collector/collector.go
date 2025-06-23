package collector

import (
	"os"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
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
func (server *Collector) Run() {
	host := ""
	port := "8080"
	if h := os.Getenv(ctrl.CollectorHostEnvName); h != "" {
		host = h
	}
	if p := os.Getenv(ctrl.CollectorPortEnvName); p != "" {
		port = p
	}
	server.router.Run(host + ":" + port)
}
