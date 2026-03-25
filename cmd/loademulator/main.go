package main

import (
	"fmt"
	"os"
	"strconv"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/control-loop/pkg/loademulator"
)

var (
	DefaultIntervalSec int     = 60
	DefaultAlpha       float64 = 0.1
	DefaultTheta       float64 = 0.2
	DefaultSkew        float64 = 0.3
)

func main() {
	// provide help
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Env vars: " +
			ctrl.LoadIntervalEnvName + " " +
			ctrl.LoadAlphaEnvName + " " +
			ctrl.LoadThetaEnvName + " " +
			ctrl.LoadSkewEnvName)
		return
	}

	// get config from env vars (fall back to defaults)
	interval := DefaultIntervalSec
	alpha := DefaultAlpha
	theta := DefaultTheta
	skew := DefaultSkew

	if s := os.Getenv(ctrl.LoadIntervalEnvName); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadIntervalEnvName + ": " + s)
			return
		}
		interval = v
	}
	if s := os.Getenv(ctrl.LoadAlphaEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadAlphaEnvName + ": " + s)
			return
		}
		alpha = v
	}
	if s := os.Getenv(ctrl.LoadThetaEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadThetaEnvName + ": " + s)
			return
		}
		theta = v
	}
	if s := os.Getenv(ctrl.LoadSkewEnvName); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			fmt.Println("bad env variable " + ctrl.LoadSkewEnvName + ": " + s)
			return
		}
		skew = v
	}

	fmt.Println("Running with interval=" + strconv.Itoa(interval) + "(sec), alpha=" + strconv.FormatFloat(alpha, 'f', 3, 64) +
		", theta=" + strconv.FormatFloat(theta, 'f', 3, 64) +
		", skew=" + strconv.FormatFloat(skew, 'f', 3, 64))

	// run emulator
	lg, err := loademulator.NewLoadEmulator(interval, alpha, theta, skew)
	if err != nil {
		fmt.Println(err)
		return
	}
	lg.Run()
}
