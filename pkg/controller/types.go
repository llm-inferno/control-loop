package controller

import "github.com/llm-inferno/optimizer-light/pkg/config"

// Inference server information related to Kubernetes
type ServerKubeInfo struct {
	UID   string `json:"uid"`   // unique ID of object
	Name  string `json:"name"`  // name of object
	Space string `json:"space"` // name space of object
}

// Inference server information collected
type ServerCollectorInfo struct {
	Spec         []config.ServerSpec       `json:"servers"`
	ReplicaSpecs []config.ServerSpec       `json:"replicas"`       // one entry per running pod
	KubeResource map[string]ServerKubeInfo `json:"kube-resources"` // map of server names to kubeInfo
}

// CalibrationStatus mirrors the tuner's per-(model, accelerator) calibration trigger facts
// (GET /calibration-status). The controller acts on NeedsCalibration.
type CalibrationStatus struct {
	Model            string  `json:"model"`
	Accelerator      string  `json:"accelerator"`
	StorePresent     bool    `json:"storePresent"`
	Calibrated       bool    `json:"calibrated"`
	ObsCount         int     `json:"obsCount"`
	ObsTarget        int     `json:"obsTarget"`
	ConditionNumber  float64 `json:"conditionNumber"`
	IllConditioned   bool    `json:"illConditioned"`
	NeedsCalibration bool    `json:"needsCalibration"`
}

// Inference server information actuated
type ServerActuatorInfo struct {
	Spec         map[string]config.AllocationData `json:"allocations"`    // map of server names to allocation data
	KubeResource map[string]ServerKubeInfo        `json:"kube-resources"` // map of server names to kubeInfo
}
