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
	ArvRateRange   = [2]float64{6.0, 240.0}
	NumTokensRange = [2]int{100, 10000}
)

// Load emulator
type LoadEmulator struct {
	kubeClient     *kubernetes.Clientset
	interval       time.Duration
	alpha          float64
	arvRateSigma   map[string]float64
	inTokensSigma  map[string]float64
	outTokensSigma map[string]float64
}

// create a new load emulator
func NewLoadEmulator(intervalSec int, alpha float64) (loadEmulator *LoadEmulator, err error) {
	if intervalSec <= 0 || alpha < 0 || alpha > 1 {
		return nil, fmt.Errorf("%s", "invalid input: interval="+strconv.Itoa(intervalSec)+
			", alpha="+strconv.FormatFloat(alpha, 'f', 3, 64))
	}
	var kubeClient *kubernetes.Clientset
	if kubeClient, err = ctrl.GetKubeClient(); err == nil {
		return &LoadEmulator{
			kubeClient:     kubeClient,
			interval:       time.Duration(intervalSec) * time.Second,
			alpha:          alpha,
			arvRateSigma:   map[string]float64{},
			inTokensSigma:  map[string]float64{},
			outTokensSigma: map[string]float64{},
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
			curRPM, _ := strconv.ParseFloat(d.Labels[ctrl.KeyArrivalRate], 64)
			curInTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyInTokens])
			curOutTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyOutTokens])

			// perturb arrival rates and number of tokens randomly
			lg.perturbLoad(string(d.GetUID()), &curRPM, &curInTokens, &curOutTokens)

			// update labels
			d.Labels[ctrl.KeyArrivalRate] = fmt.Sprintf("%.4f", curRPM)
			d.Labels[ctrl.KeyInTokens] = fmt.Sprintf("%d", curInTokens)
			d.Labels[ctrl.KeyOutTokens] = fmt.Sprintf("%d", curOutTokens)
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
func (lg *LoadEmulator) perturbLoad(uid string, rpm *float64, inTok *int, outTok *int) {
	// store original values if new entry
	if _, exists := lg.arvRateSigma[uid]; !exists {
		lg.arvRateSigma[uid] = (*rpm) * lg.alpha
	}
	if _, exists := lg.inTokensSigma[uid]; !exists {
		lg.inTokensSigma[uid] = float64(*inTok) * lg.alpha
	}
	if _, exists := lg.outTokensSigma[uid]; !exists {
		lg.outTokensSigma[uid] = float64(*outTok) * lg.alpha
	}

	// generate a random number from a standard normal distribution
	// TODO: should use two random number generators
	sampleRPM := rand.NormFloat64()
	sampleInTok := rand.NormFloat64()
	sampleOutTok := rand.NormFloat64()

	newArv := sampleRPM*lg.arvRateSigma[uid] + *rpm
	newArv = min(max(newArv, ArvRateRange[0]), ArvRateRange[1])
	*rpm = newArv

	newInTok := int(math.Ceil(sampleInTok*lg.inTokensSigma[uid] + float64(*inTok)))
	newInTok = min(max(newInTok, NumTokensRange[0]), NumTokensRange[1])
	*inTok = newInTok

	newOutTok := int(math.Ceil(sampleOutTok*lg.outTokensSigma[uid] + float64(*outTok)))
	newOutTok = min(max(newOutTok, NumTokensRange[0]), NumTokensRange[1])
	*outTok = newOutTok
}
