package main

import (
	"os"

	rest "github.com/llm-inferno/inferno/rest-server"
)

// create and run a REST API Optimizer server
//   - stateless (default) or statefull (with -F argument)
func main() {
	var server rest.RESTServer
	statefull := len(os.Args) > 1 && os.Args[1] == rest.DefaultStatefull
	if statefull {
		server = rest.NewStateFullServer()
	} else {
		server = rest.NewStateLessServer()
	}
	server.Run()
}
