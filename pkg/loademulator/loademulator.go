package loademulator

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	ArvRateRange   = [2]float32{6.0, 240.0}
	NumTokensRange = [2]int{100, 10000}
)

// Load emulator
type LoadEmulator struct {
	kubeClient     *kubernetes.Clientset
	interval       time.Duration
	alpha          float32
	arvRateSigma   map[string]float32
	numTokensSigma map[string]float32
}

// create a new load emulator
func NewLoadEmulator(intervalSec int, alpha float32) (loadEmulator *LoadEmulator, err error) {
	if intervalSec <= 0 || alpha < 0 || alpha > 1 {
		return nil, fmt.Errorf("%s", "invalid input: interval="+strconv.Itoa(intervalSec)+
			", alpha="+strconv.FormatFloat(float64(alpha), 'f', 3, 32))
	}
	var kubeClient *kubernetes.Clientset
	if kubeClient, err = ctrl.GetKubeClient(); err == nil {
		return &LoadEmulator{
			kubeClient:     kubeClient,
			interval:       time.Duration(intervalSec) * time.Second,
			alpha:          alpha,
			arvRateSigma:   map[string]float32{},
			numTokensSigma: map[string]float32{},
		}, nil
	}
	return nil, err
}

// run the load emulator
func (lg *LoadEmulator) Run() {
	for {
		fmt.Println("Waiting " + lg.interval.String() + "...")
		time.Sleep(time.Duration(lg.interval))

		// get deployments
		labelSelector := ctrl.KeyManaged + "=true"
		deps, err := lg.kubeClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector})
		if err != nil {
			fmt.Println(err)
			continue
		}

		// update deployments
		for _, d := range deps.Items {
			curRPM64, _ := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 32)
			curRPM := float32(curRPM64)
			curNumTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyNumTokens])

			// perturb arrival rates and number of tokens randomly
			lg.perturbLoad(string(d.GetUID()), &curRPM, &curNumTokens)

			// update labels
			d.Labels[ctrl.KeyArrivalRate] = fmt.Sprintf("%.4f", curRPM)
			d.Labels[ctrl.KeyNumTokens] = fmt.Sprintf("%d", curNumTokens)
			if _, err := lg.kubeClient.AppsV1().Deployments(d.Namespace).Update(context.TODO(), &d, metav1.UpdateOptions{}); err != nil {
				fmt.Println(err)
				continue
			}
		}
		fmt.Printf("%d deployment(s) updated\n", len(deps.Items))
	}
}

/*
 * randomly modify dynamic server data (testing only)
 */

// generate: nextValue = currentValue + normal(0, sigma),
// where sigma = alpha * originalValue and 0 <= alpha <= 1
func (lg *LoadEmulator) perturbLoad(uid string, rpm *float32, num *int) {
	// store original values if new entry
	if _, exists := lg.arvRateSigma[uid]; !exists {
		lg.arvRateSigma[uid] = (*rpm) * lg.alpha
	}
	if _, exists := lg.numTokensSigma[uid]; !exists {
		lg.numTokensSigma[uid] = float32(*num) * lg.alpha
	}

	// generate a random number from a standard normal distribution
	// TODO: should use two random number generators
	sampleRPM := float32(rand.NormFloat64())
	sampleTokens := float32(rand.NormFloat64())

	newArv := sampleRPM*lg.arvRateSigma[uid] + *rpm
	if newArv < ArvRateRange[0] {
		newArv = ArvRateRange[0]
	}
	if newArv > ArvRateRange[1] {
		newArv = ArvRateRange[1]
	}
	*rpm = newArv

	newLength := int(int(math.Ceil(float64(sampleTokens*lg.numTokensSigma[uid] + float32(*num)))))
	if newLength < NumTokensRange[0] {
		newLength = NumTokensRange[0]
	}
	if newLength > NumTokensRange[1] {
		newLength = NumTokensRange[1]
	}
	*num = newLength
}
