package controller

import (
	"time"
)

// Environment names for hosts and ports
const (
	ControllerHostEnvName = "CONTROLLER_HOST"
	ControllerPortEnvName = "CONTROLLER_PORT"

	CollectorHostEnvName = "COLLECTOR_HOST"
	CollectorPortEnvName = "COLLECTOR_PORT"

	ActuatorHostEnvName = "ACTUATOR_HOST"
	ActuatorPortEnvName = "ACTUATOR_PORT"

	OptimizerHostEnvName = "INFERNO_HOST"
	OptimizerPortEnvName = "INFERNO_PORT"

	TunerHostEnvName = "TUNER_HOST"
	TunerPortEnvName = "TUNER_PORT"

	DataPathEnvName       = "INFERNO_DATA_PATH"
	ControlPeriodEnvName  = "INFERNO_CONTROL_PERIOD"
	ControlDynamicEnvName = "INFERNO_CONTROL_DYNAMIC"

	LoadIntervalEnvName = "INFERNO_LOAD_INTERVAL"
	LoadAlphaEnvName    = "INFERNO_LOAD_ALPHA"
	LoadThetaEnvName    = "INFERNO_LOAD_THETA"
	LoadSkewEnvName     = "INFERNO_LOAD_SKEW"

	StartupDelayEnvName  = "INFERNO_STARTUP_DELAY"
	WarmUpTimeoutEnvName = "INFERNO_WARM_UP_TIMEOUT"
)

// Default host and port for each REST server
const (
	DefaultControllerHost = ""
	DefaultControllerPort = "8080"

	DefaultCollectorHost = ""
	DefaultCollectorPort = "8080"

	DefaultActuatorHost = ""
	DefaultActuatorPort = "8080"

	DefaultTunerHost = "localhost"
	DefaultTunerPort = "8081"
)

const (
	// path to static data json files (ends with /)
	DefaultDataPath = "./"

	// static data file names
	AcceleratorFileName  = "accelerator-data.json"
	CapacityFileName     = "capacity-data.json"
	ModelFileName        = "model-data.json"
	ServiceClassFileName = "serviceclass-data.json"
	OptimizerFileName    = "optimizer-data.json"

	// API settings
	OptimizeVerb = "optimizeOne"
	ServersVerb  = "getServers"
	CollectVerb  = "collect"
	ActuatorVerb = "update"
	TuneVerb    = "tune"
	MergeVerb   = "merge"
	WarmUpVerb  = "warmup"

	// others
	DefaultControlPeriodSeconds int  = 60 // periodicity of control (zero means aperiodic)
	DefaultControlDynamicMode   bool = false
	DefaultStartupDelaySec      int  = 0  // seconds to wait after pod start before treating it as ready
	DefaultWarmUpTimeout        int  = 10 // max consecutive warm-up cycles before proceeding (0 = no timeout)

	ServerSimPort = 8080 // server-sim sidecar listen port

	ReplicaNameSeparator = "/" // separator between server name and pod name in replica specs
)

// Kube config
const (
	KubeConfigEnvName = "KUBECONFIG"
	DefaulKubeConfig  = "$HOME/.kube/config"
)

// Key labels
// TODO: remove load data from labels, get from Prometheus
const (
	KeyPrefix           = "inferno."
	KeyServerPrefix     = KeyPrefix + "server."
	KeyAllocationPrefix = KeyServerPrefix + "allocation."
	KeyLoadPrefix       = KeyServerPrefix + "load."

	KeyManaged     = KeyServerPrefix + "managed"
	KeyServerName  = KeyServerPrefix + "name"
	KeyServerModel = KeyServerPrefix + "model"
	KeyServerClass = KeyServerPrefix + "class"

	KeyAccelerator  = KeyAllocationPrefix + "accelerator"
	KeyMaxBatchSize = KeyAllocationPrefix + "maxbatchsize"

	KeyArrivalRate = KeyLoadPrefix + "rpm"
	KeyInTokens    = KeyLoadPrefix + "intokens"
	KeyOutTokens   = KeyLoadPrefix + "outtokens"

	KeyNominalPrefix      = KeyLoadPrefix + "nominal."
	KeyNominalArrivalRate = KeyNominalPrefix + "rpm"
	KeyNominalInTokens    = KeyNominalPrefix + "intokens"
	KeyNominalOutTokens   = KeyNominalPrefix + "outtokens"
)

var (
	CollectorURL string
	OptimizerURL string
	ActuatorURL  string
	TunerURL     string

	DataPath     string
	StartupDelay time.Duration // how long to wait after pod StartTime before treating pod as ready
)


