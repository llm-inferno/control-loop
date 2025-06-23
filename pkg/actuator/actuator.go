package actuator

import (
	"os"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
)

// Kube client as global variable, used by handler functions
var KubeClient *kubernetes.Clientset

// Actuator REST server
type Actuator struct {
	router *gin.Engine
}

// create a new Actuator
func NewActuator() (actuator *Actuator, err error) {
	if KubeClient, err = ctrl.GetKubeClient(); err != nil {
		return nil, err
	}
	actuator = &Actuator{
		router: gin.Default(),
	}
	actuator.router.POST("/update", update)
	return actuator, nil
}

// start server
func (server *Actuator) Run() {
	host := ""
	port := "8080"
	if h := os.Getenv(ctrl.ActuatorHostEnvName); h != "" {
		host = h
	}
	if p := os.Getenv(ctrl.ActuatorPortEnvName); p != "" {
		port = p
	}
	server.router.Run(host + ":" + port)
}
