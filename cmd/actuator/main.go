package main

import (
	"fmt"
	"os"

	"github.com/llm-inferno/control-loop/pkg/actuator"
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
)

// create and run an Actuator server
func main() {
	host := os.Getenv(ctrl.ActuatorHostEnvName)
	if host == "" {
		host = ctrl.DefaultActuatorHost
	}
	port := os.Getenv(ctrl.ActuatorPortEnvName)
	if port == "" {
		port = ctrl.DefaultActuatorPort
	}

	actuator, err := actuator.NewActuator()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	actuator.Run(host, port)
}
