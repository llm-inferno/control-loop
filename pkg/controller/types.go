package controller

import "github.com/llm-inferno/inferno/pkg/config"

// Inference server information related to Kubernetes
type ServerKubeInfo struct {
	UID   string `json:"uid"`   // unique ID of object
	Name  string `json:"name"`  // name of object
	Space string `json:"space"` // name space of object
}

// Inference server information collected
type ServerCollectorInfo struct {
	Spec         []config.ServerSpec       `json:"servers"`
	KubeResource map[string]ServerKubeInfo `json:"kube-resources"` // map of server names to kubeInfo
}

// Inference server information actuated
type ServerActuatorInfo struct {
	Spec         map[string]config.AllocationData `json:"allocations"`    // map of server names to allocation data
	KubeResource map[string]ServerKubeInfo        `json:"kube-resources"` // map of server names to kubeInfo
}
