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

	// WatchNamespaceEnvName scopes the managed-deployment watch to a single
	// namespace. Empty/unset means cluster-wide (default; backwards compatible).
	WatchNamespaceEnvName = "WATCH_NAMESPACE"

	DataPathEnvName            = "INFERNO_DATA_PATH"
	DefaultMaxBatchSizeEnvName = "DEFAULT_MAX_BATCH_SIZE"
	ControlPeriodEnvName       = "INFERNO_CONTROL_PERIOD"
	ControlDynamicEnvName      = "INFERNO_CONTROL_DYNAMIC"

	LoadIntervalEnvName = "INFERNO_LOAD_INTERVAL"
	LoadAlphaEnvName    = "INFERNO_LOAD_ALPHA"
	LoadThetaEnvName    = "INFERNO_LOAD_THETA"
	LoadSkewEnvName     = "INFERNO_LOAD_SKEW"
	LoadPhasesEnvName   = "INFERNO_LOAD_PHASES"

	StartupDelayEnvName  = "INFERNO_STARTUP_DELAY"
	WarmUpTimeoutEnvName = "INFERNO_WARM_UP_TIMEOUT"

	// Benchmarking-on-the-fly calibration. The trigger runs only when
	// CalibrationEnabledEnvName is truthy AND the tuner reports a pair needs it
	// (natural warm-up excitation was insufficient and no calibration has succeeded yet).
	CalibrationEnabledEnvName   = "INFERNO_CALIBRATION_ENABLED"     // "true"/"1" enables the trigger
	CalibRPMFactorsEnvName      = "INFERNO_CALIB_RPM_FACTORS"       // comma-separated multipliers of nominal RPM
	CalibPointTimeoutSecEnvName = "INFERNO_CALIB_POINT_TIMEOUT_SEC" // per-sweep-point /simulate timeout
	CalibPollIntervalSecEnvName = "INFERNO_CALIB_POLL_INTERVAL_SEC" // /simulate/:id poll interval

	// BackendEnvName selects the sense/actuate backend for this process:
	// "serversim" (default) or "llmd". Applies to every managed deployment
	// the process handles.
	BackendEnvName = "INFERNO_BACKEND"

	// Prometheus client configuration (used by the collector). When unset the
	// collector falls back to http://localhost:9090 with no auth (server-sim).
	PrometheusURLEnvName       = "INFERNO_PROMETHEUS_URL"        // e.g. https://thanos-querier.openshift-monitoring.svc:9091
	PrometheusTokenPathEnvName = "INFERNO_PROMETHEUS_TOKEN_PATH" // default: SA token path
	PrometheusCAPathEnvName    = "INFERNO_PROMETHEUS_CA_PATH"    // default: SA CA path
	PrometheusInsecureEnvName  = "INFERNO_PROMETHEUS_INSECURE"   // "true" => skip TLS verify
	PrometheusWindowEnvName    = "INFERNO_PROMETHEUS_WINDOW"     // PromQL range window, default "1m"
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
	OptimizeVerb          = "optimizeOne"
	ServersVerb           = "getServers"
	CollectVerb           = "collect"
	ActuatorVerb          = "update"
	TuneVerb              = "tune"
	MergeVerb             = "merge"
	WarmUpVerb            = "warmup"
	CalibrateVerb         = "calibrate"          // tuner: POST batch sweep fit
	CalibrationStatusVerb = "calibration-status" // tuner: GET per-pair trigger facts
	SweepVerb             = "sweep"              // collector: GET runs the load sweep for a server

	// others
	DefaultControlPeriodSeconds int  = 60 // periodicity of control (zero means aperiodic)
	DefaultControlDynamicMode   bool = false
	DefaultStartupDelaySec      int  = 0  // seconds to wait after pod start before treating it as ready
	DefaultWarmUpTimeout        int  = 10 // max consecutive warm-up cycles before proceeding (0 = no timeout)

	// Calibration sweep defaults. Factors are skewed BELOW nominal: on a high-nominal workload
	// (load already near the single-replica knee) factors >= ~1.5 saturate and get dropped,
	// starving the fit. Probing below nominal keeps most points unsaturated and usable.
	DefaultCalibRPMFactors      = "0.25,0.5,0.75,1.0" // multipliers of nominal RPM swept at base token mix
	DefaultCalibPointTimeoutSec = 120                 // per-point /simulate timeout (blis DES can be slow)
	DefaultCalibPollIntervalSec = 2                   // /simulate/:id poll interval

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
	KeyMaxQueueSize = KeyAllocationPrefix + "maxqueuesize"

	KeyArrivalRate = KeyLoadPrefix + "rpm"
	KeyInTokens    = KeyLoadPrefix + "intokens"
	KeyOutTokens   = KeyLoadPrefix + "outtokens"

	KeyNominalPrefix      = KeyLoadPrefix + "nominal."
	KeyNominalArrivalRate = KeyNominalPrefix + "rpm"
	KeyNominalInTokens    = KeyNominalPrefix + "intokens"
	KeyNominalOutTokens   = KeyNominalPrefix + "outtokens"

	// Evaluator backend selection on managed Deployments
	KeyEvaluator      = KeyServerPrefix + "evaluator"
	KeyVLLMDeployment = KeyServerPrefix + "vllm-deployment"
	KeyVLLMNamespace  = KeyServerPrefix + "vllm-namespace"
	KeyPairID         = KeyServerPrefix + "pair-id"

	// Evaluator label values
	EvaluatorVLLMServer    = "vllm-server"
	EvaluatorQueueAnalysis = "queue-analysis"
	EvaluatorBlis          = "blis"
)

var (
	CollectorURL string
	OptimizerURL string
	ActuatorURL  string
	TunerURL     string

	DataPath     string
	StartupDelay time.Duration // how long to wait after pod StartTime before treating pod as ready
)
