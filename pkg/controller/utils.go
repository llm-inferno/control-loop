package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/llm-inferno/inferno/pkg/config"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// get URL of a REST server
func GetURL(hostEnvName, portEnvName string) string {
	host := "localhost"
	port := "8080"
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
		defer res.Body.Close()
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
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s", "actuator failed: "+res.Status)
		}
		return nil
	}
}
