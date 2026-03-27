package main

import (
	"fmt"
	"os"

	"github.com/llm-inferno/control-loop/pkg/collector"
	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
)

// create and run a Collector server
func main() {
	host := os.Getenv(ctrl.CollectorHostEnvName)
	if host == "" {
		host = ctrl.DefaultCollectorHost
	}
	port := os.Getenv(ctrl.CollectorPortEnvName)
	if port == "" {
		port = ctrl.DefaultCollectorPort
	}

	collector, err := collector.NewCollector()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	collector.Run(host, port)
}
