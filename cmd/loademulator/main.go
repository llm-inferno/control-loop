package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/llm-inferno/control-loop/pkg/loademulator"
)

var (
	DefaultIntervalSec int     = 60
	DefaultAlpha       float32 = 0.5
)

func main() {
	// provide help
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Args: " + " <intervalInSec>" + " <alpha (0,1)>")
		return
	}

	// get args
	interval := DefaultIntervalSec
	alpha := DefaultAlpha
	var err error
	switch len(os.Args) {
	case 2:
		if interval, err = strconv.Atoi(os.Args[1]); err != nil {
			fmt.Println(err)
			return
		}
	case 3:
		if interval, err = strconv.Atoi(os.Args[1]); err != nil {
			fmt.Println(err)
			return
		}
		if alpha64, err := strconv.ParseFloat(os.Args[2], 32); err != nil {
			fmt.Println(err)
			return
		} else {
			alpha = float32(alpha64)
		}
	}
	fmt.Println("Running with interval=" + strconv.Itoa(interval) + "(sec) and alpha=" + strconv.FormatFloat(float64(alpha), 'f', 3, 32))

	// run emulator
	lg, err := loademulator.NewLoadEmulator(interval, alpha)
	if err != nil {
		fmt.Println(err)
		return
	}
	lg.Run()
}
