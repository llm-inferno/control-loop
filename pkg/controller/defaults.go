package controller

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

	DataPathEnvName       = "INFERNO_DATA_PATH"
	ControlPeriodEnvName  = "INFERNO_CONTROL_PERIOD"
	ControlDynamicEnvName = "INFERNO_CONTROL_DYNAMIC"
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

	// others
	DefaultControlPeriodSeconds int  = 60 // periodicity of control (zero means aperiodic)
	DefaultControlDynamicMode   bool = false
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
	KeyNumTokens   = KeyLoadPrefix + "numtokens"
)

var (
	CollectorURL string
	OptimizerURL string
	ActuatorURL  string

	DataPath string
)
