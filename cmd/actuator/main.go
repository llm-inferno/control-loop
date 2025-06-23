package main

import (
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/actuator"
)

// create and run an Actuator server
func main() {
	actuator, err := actuator.NewActuator()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	actuator.Run()
}
