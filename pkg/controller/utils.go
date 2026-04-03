package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/llm-inferno/optimizer-light/pkg/config"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// get URL of a REST server
func GetURL(hostEnvName, portEnvName string) string {
	return GetURLWithDefaults(hostEnvName, portEnvName, "localhost", "8080")
}

// get URL of a REST server with explicit default host and port
func GetURLWithDefaults(hostEnvName, portEnvName, defaultHost, defaultPort string) string {
	host := defaultHost
	port := defaultPort
	if h := os.Getenv(hostEnvName); h != "" {
		host = h
	}
	if p := os.Getenv(portEnvName); p != "" {
		port = p
	}
	return "http://" + host + ":" + port
}

// get a Kubernetes client
func GetKubeClient() (client *kubernetes.Clientset, err error) {
	kubeconfigPath := os.Getenv(KubeConfigEnvName)

	var kubeconfig *rest.Config
	if kubeconfigPath != "" {
		if kubeconfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath); err != nil {
			return nil, err
		}
		fmt.Println("Running external to the cluster using " + kubeconfigPath)
	} else {
		if kubeconfig, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
		fmt.Println("Running internal in the cluster")
	}

	if client, err = kubernetes.NewForConfig(kubeconfig); err != nil {
		return nil, err
	}
	fmt.Println("Kube client created")
	return client, nil
}

// get server data by sending GET to Collector
func GETCollectorInfo() (*ServerCollectorInfo, error) {
	endPoint := CollectorURL + "/" + CollectVerb
	response, getErr := http.Get(endPoint)
	if getErr != nil {
		return nil, getErr
	}
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	collectorInfo := ServerCollectorInfo{}
	jsonErr := json.Unmarshal(body, &collectorInfo)
	if jsonErr != nil {
		return nil, jsonErr
	}
	return &collectorInfo, nil
}

// get optimizer solution by sending POST to REST server
func POSTOptimize(systemData *config.SystemData) (*config.AllocationSolution, error) {
	endPoint := OptimizerURL + "/" + OptimizeVerb
	if byteValue, err := json.Marshal(systemData); err != nil {
		return nil, err
	} else {
		req, getErr := http.NewRequest("POST", endPoint, bytes.NewBuffer(byteValue))
		if getErr != nil {
			return nil, getErr
		}
		req.Header.Add("Content-Type", "application/json")
		client := &http.Client{}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s", "optimize failed to find solution: "+res.Status)
		}
		solution := config.AllocationSolution{}
		derr := json.NewDecoder(res.Body).Decode(&solution)
		if derr != nil {
			return nil, derr
		}
		return &solution, nil
	}
}

// get servers data from REST server
func GetServerData() (*config.ServerData, error) {
	endPoint := OptimizerURL + "/" + ServersVerb
	response, getErr := http.Get(endPoint)
	if getErr != nil {
		return nil, getErr
	}
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	servers := config.ServerData{}
	jsonErr := json.Unmarshal(body, &servers)
	if jsonErr != nil {
		return nil, jsonErr
	}
	return &servers, nil
}

// send replica specs to Tuner and get tuned model data
func POSTTune(replicaSpecs []config.ServerSpec) (*config.ModelData, error) {
	endPoint := TunerURL + "/" + TuneVerb
	if byteValue, err := json.Marshal(replicaSpecs); err != nil {
		return nil, err
	} else {
		req, getErr := http.NewRequest("POST", endPoint, bytes.NewBuffer(byteValue))
		if getErr != nil {
			return nil, getErr
		}
		req.Header.Add("Content-Type", "application/json")
		client := &http.Client{}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s", "tuner /tune failed: "+res.Status)
		}
		modelData := config.ModelData{}
		derr := json.NewDecoder(res.Body).Decode(&modelData)
		if derr != nil {
			return nil, derr
		}
		return &modelData, nil
	}
}

// send current model data to Tuner and get merged model data
func POSTMerge(modelData *config.ModelData) (*config.ModelData, error) {
	endPoint := TunerURL + "/" + MergeVerb
	if byteValue, err := json.Marshal(modelData); err != nil {
		return nil, err
	} else {
		req, getErr := http.NewRequest("POST", endPoint, bytes.NewBuffer(byteValue))
		if getErr != nil {
			return nil, getErr
		}
		req.Header.Add("Content-Type", "application/json")
		client := &http.Client{}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s", "tuner /merge failed: "+res.Status)
		}
		merged := config.ModelData{}
		derr := json.NewDecoder(res.Body).Decode(&merged)
		if derr != nil {
			return nil, derr
		}
		return &merged, nil
	}
}

// query Tuner warm-up status
func GETWarmUp() (bool, error) {
	endPoint := TunerURL + "/" + WarmUpVerb
	res, err := http.Get(endPoint)
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("tuner /warmup failed: %s", res.Status)
	}
	var body struct {
		WarmingUp bool `json:"warmingUp"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.WarmingUp, nil
}

// send optimizer solution to Actuator
func POSTActuator(actuatorInfo *ServerActuatorInfo) error {
	endPoint := ActuatorURL + "/" + ActuatorVerb
	if byteValue, err := json.Marshal(actuatorInfo); err != nil {
		return err
	} else {
		req, getErr := http.NewRequest("POST", endPoint, bytes.NewBuffer(byteValue))
		if getErr != nil {
			return getErr
		}
		req.Header.Add("Content-Type", "application/json")
		client := &http.Client{}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s", "actuator failed: "+res.Status)
		}
		return nil
	}
}
