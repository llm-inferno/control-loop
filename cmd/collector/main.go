package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

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

	if s := os.Getenv(ctrl.StartupDelayEnvName); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.StartupDelayEnvName + ": " + s)
			return
		}
		ctrl.StartupDelay = time.Duration(v) * time.Second
	}

	collector, err := collector.NewCollector()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	collector.Run(host, port)
}
