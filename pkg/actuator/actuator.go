package actuator

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
)

// Kube client as global variable, used by handler functions
var KubeClient *kubernetes.Clientset

// pairingDebug is true when INFERNO_PAIRING_LOG_LEVEL=debug; enables per-tick tracing.
var pairingDebug bool

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

	pairingDebug = strings.EqualFold(os.Getenv("INFERNO_PAIRING_LOG_LEVEL"), "debug")

	// Pairing is a server-sim/vLLM-server concern; never run it in llmd mode.
	if backend.ModeFromEnv() == backend.ModeServerSim {
		period := pairingTickInterval()
		if period > 0 {
			fmt.Printf("actuator: starting pairing reconciler (tick=%s)\n", period)
			go runReconciler(context.Background(), KubeClient, period)
		} else {
			fmt.Printf("actuator: pairing reconciler disabled (INFERNO_PAIRING_TICK_SEC=0)\n")
		}
	} else {
		fmt.Println("actuator: pairing reconciler disabled (llmd backend)")
	}
	return actuator, nil
}

// pairingTickInterval reads INFERNO_PAIRING_TICK_SEC; defaults to 5s, returns 0
// if the env var is set to "0" (disable).
func pairingTickInterval() time.Duration {
	const defaultSec = 5
	v := os.Getenv("INFERNO_PAIRING_TICK_SEC")
	if v == "" {
		return defaultSec * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		fmt.Printf("actuator: invalid INFERNO_PAIRING_TICK_SEC=%q; using default %ds\n", v, defaultSec)
		return defaultSec * time.Second
	}
	return time.Duration(n) * time.Second
}

// start server
func (server *Actuator) Run(host, port string) {
	_ = server.router.Run(host + ":" + port)
}
