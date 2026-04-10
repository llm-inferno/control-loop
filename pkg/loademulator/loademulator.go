package loademulator

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	// LoadRangeFactor is the multiplicative factor used to derive min/max bounds from
	// nominal values: min = nominal/factor, max = nominal*factor (symmetric in log space).
	LoadRangeFactor = 10.0

	// LoadRangeTokensFloor is the minimum allowed value for token counts.
	LoadRangeTokensFloor = 1
)

// Load emulator
type LoadEmulator struct {
	kubeClient *kubernetes.Clientset
	interval   time.Duration
	alpha      float64
	theta      float64
	skew       float64
}

// create a new load emulator
func NewLoadEmulator(intervalSec int, alpha, theta, skew float64) (loadEmulator *LoadEmulator, err error) {
	if intervalSec <= 0 || alpha < 0 || alpha > 1 || theta < 0 || theta > 1 || skew < 0 || skew > 1 {
		return nil, fmt.Errorf("%s", "invalid input: interval="+strconv.Itoa(intervalSec)+
			", alpha="+strconv.FormatFloat(alpha, 'f', 3, 64)+
			", theta="+strconv.FormatFloat(theta, 'f', 3, 64)+
			", skew="+strconv.FormatFloat(skew, 'f', 3, 64))
	}
	var kubeClient *kubernetes.Clientset
	if kubeClient, err = ctrl.GetKubeClient(); err == nil {
		return &LoadEmulator{
			kubeClient: kubeClient,
			interval:   time.Duration(intervalSec) * time.Second,
			alpha:      alpha,
			theta:      theta,
			skew:       skew,
		}, nil
	}
	return nil, err
}

// run the load emulator
func (lg *LoadEmulator) Run() {
	for {

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

			nomRPM, _ := strconv.ParseFloat(d.Labels[ctrl.KeyNominalArrivalRate], 64)
			nomInTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyNominalInTokens])
			nomOutTokens, _ := strconv.Atoi(d.Labels[ctrl.KeyNominalOutTokens])

			// perturb arrival rates and number of tokens randomly
			lg.perturbLoad(&curRPM, &curInTokens, &curOutTokens, nomRPM, nomInTokens, nomOutTokens)

			// update deployment labels
			d.Labels[ctrl.KeyArrivalRate] = fmt.Sprintf("%.4f", curRPM)
			d.Labels[ctrl.KeyInTokens] = fmt.Sprintf("%d", curInTokens)
			d.Labels[ctrl.KeyOutTokens] = fmt.Sprintf("%d", curOutTokens)
			if _, err := lg.kubeClient.AppsV1().Deployments(d.Namespace).Update(context.TODO(), &d, metav1.UpdateOptions{}); err != nil {
				fmt.Println(err)
				continue
			}

			// update pod labels
			selectorStr := labels.Set(d.Spec.Selector.MatchLabels).String()
			if err := lg.updatePodLabels(d.Namespace, selectorStr, d.UID, curRPM, curInTokens, curOutTokens); err != nil {
				fmt.Println(err)
			}
		}
		fmt.Printf("%d deployment(s) updated\n", len(deps.Items))
		fmt.Println("Waiting " + lg.interval.String() + "...")
		time.Sleep(time.Duration(lg.interval))
	}
}

// update pod labels: split totalRPM across pods using skew, broadcast token counts
// pods are resolved via the ownership chain: Deployment UID → ReplicaSet → Pods
func (lg *LoadEmulator) updatePodLabels(namespace, selectorStr string, deploymentUID types.UID, totalRPM float64, inTokens, outTokens int) error {
	// find ReplicaSets owned by this deployment
	rsList, err := lg.kubeClient.AppsV1().ReplicaSets(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selectorStr})
	if err != nil {
		return err
	}
	rsUIDs := make(map[types.UID]struct{})
	for _, rs := range rsList.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.UID == deploymentUID {
				rsUIDs[rs.UID] = struct{}{}
				break
			}
		}
	}

	// find pods owned by those ReplicaSets
	pods, err := lg.kubeClient.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selectorStr})
	if err != nil {
		return err
	}
	// filter to running pods owned by this deployment's ReplicaSets
	running := make([]int, 0, len(pods.Items))
	for i, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		owned := false
		for _, owner := range p.OwnerReferences {
			if _, ok := rsUIDs[owner.UID]; ok {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if !ctrl.IsPodReady(p.Status.StartTime) {
			fmt.Printf("pod %s: skipping (within startup delay)\n", p.Name)
			continue
		}
		running = append(running, i)
	}
	if len(running) == 0 {
		return nil
	}
	podRPMs := lg.splitLoad(totalRPM, len(running))
	for j, i := range running {
		p := pods.Items[i]
		p.Labels[ctrl.KeyArrivalRate] = fmt.Sprintf("%.4f", podRPMs[j])
		p.Labels[ctrl.KeyInTokens] = fmt.Sprintf("%d", inTokens)
		p.Labels[ctrl.KeyOutTokens] = fmt.Sprintf("%d", outTokens)
		if _, err := lg.kubeClient.CoreV1().Pods(p.Namespace).Update(context.TODO(), &p, metav1.UpdateOptions{}); err != nil {
			fmt.Println(err)
		}
	}
	return nil
}

/*
 * randomly modify dynamic server data (testing only)
 */

// generate: nextValue = currentValue + theta*(nominal - currentValue) + Normal(0, alpha*nominal)
// mean-reverting random walk: time average converges to nominal
func (lg *LoadEmulator) perturbLoad(rpm *float64, inTok *int, outTok *int, nomRPM float64, nomInTok, nomOutTok int) {
	newArv := *rpm + lg.theta*(nomRPM-*rpm) + rand.NormFloat64()*lg.alpha*nomRPM
	*rpm = min(max(newArv, nomRPM/LoadRangeFactor), nomRPM*LoadRangeFactor)

	tokInMin := max(int(math.Ceil(float64(nomInTok)/LoadRangeFactor)), LoadRangeTokensFloor)
	newInTok := float64(*inTok) + lg.theta*(float64(nomInTok)-float64(*inTok)) + rand.NormFloat64()*lg.alpha*float64(nomInTok)
	*inTok = min(max(int(math.Ceil(newInTok)), tokInMin), int(float64(nomInTok)*LoadRangeFactor))

	tokOutMin := max(int(math.Ceil(float64(nomOutTok)/LoadRangeFactor)), LoadRangeTokensFloor)
	newOutTok := float64(*outTok) + lg.theta*(float64(nomOutTok)-float64(*outTok)) + rand.NormFloat64()*lg.alpha*float64(nomOutTok)
	*outTok = min(max(int(math.Ceil(newOutTok)), tokOutMin), int(float64(nomOutTok)*LoadRangeFactor))
}

// split totalRPM across n pods using skew factor
// skew=0: equal split; skew=1: fully random split
func (lg *LoadEmulator) splitLoad(totalRPM float64, n int) []float64 {
	weights := make([]float64, n)
	sum := 0.0
	for i := range weights {
		weights[i] = (1-lg.skew)/float64(n) + lg.skew*rand.Float64()
		sum += weights[i]
	}
	rpms := make([]float64, n)
	for i := range rpms {
		rpms[i] = totalRPM * weights[i] / sum
	}
	return rpms
}
