package main

import (
	"fmt"

	"github.com/llm-inferno/control-loop/pkg/collector"
)

// create and run a Collector server
func main() {
	collector, err := collector.NewCollector()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	collector.Run()
}
