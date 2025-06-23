package main

import (
	"fmt"
	"os"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
)

// create and run a Controller
func main() {
	// provide help
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Args: " + " <controlPeriodInSec>" + " <isDynamicMode>")
		fmt.Println("Args override env variables: " +
			ctrl.ControlPeriodEnvName + "  " + ctrl.ControlDynamicEnvName)
		return
	}

	// get args
	period := ctrl.DefaultControlPeriodSeconds
	isDynamicMode := ctrl.DefaultControlDynamicMode

	// no args, use env variables, otherwise default values
	if len(os.Args) == 1 {
		if periodEnvStr := os.Getenv(ctrl.ControlPeriodEnvName); periodEnvStr != "" {
			if periodEnv, err := strconv.Atoi(periodEnvStr); err == nil && periodEnv >= 0 {
				period = periodEnv
			} else {
				fmt.Println("bad env variable " + ctrl.ControlPeriodEnvName + ": " + periodEnvStr)
				return
			}
		}
		if isDynamicStr := os.Getenv(ctrl.ControlDynamicEnvName); isDynamicStr != "" {
			if dynamic, err := strconv.ParseBool(isDynamicStr); err == nil {
				isDynamicMode = dynamic
			} else {
				fmt.Println("bad env variable " + ctrl.ControlDynamicEnvName + ": " + isDynamicStr)
				return
			}
		}
	}

	// first arg <controlPeriodInsec> given
	if len(os.Args) > 1 {
		if periodArg, err := strconv.Atoi(os.Args[1]); err == nil && periodArg >= 0 {
			period = periodArg
		} else {
			fmt.Println("bad argument <controlPeriodInSec> " + os.Args[1])
			return
		}
	}
	fmt.Printf("Running with control period = %d sec\n", period)

	// second arg <isDynamicMode> given
	if len(os.Args) > 2 {
		if dynamic, err := strconv.ParseBool(os.Args[2]); err == nil {
			isDynamicMode = dynamic
		} else {
			fmt.Println("bad argument <isDynamicMode> " + os.Args[2])
			return
		}
	}
	fmt.Printf("Running in dynamic mode = %v\n", isDynamicMode)

	controller, err := ctrl.NewController(isDynamicMode)
	if err != nil {
		fmt.Println(err)
		return
	}

	if err := controller.Init(); err != nil {
		fmt.Println(err)
		return
	}

	controller.Run(period)
	ctrl.Wg.Wait()
}
