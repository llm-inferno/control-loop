// Package backend abstracts the per-deployment sense and actuate operations
// so the control loop can run against different serving environments
// (server-sim simulators vs a real llm-d deployment). The collector and
// actuator are separate processes with no shared runtime state, so the seam
// is two interfaces selected independently in each process by INFERNO_BACKEND.
package backend

import (
	"context"
	"os"
	"strings"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
	"github.com/llm-inferno/optimizer-light/pkg/config"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"
)

// Mode is the selected backend for a process.
type Mode string

const (
	ModeServerSim Mode = "serversim"
	ModeLLMD      Mode = "llmd"
)

// ModeFromEnv reads INFERNO_BACKEND. Anything other than an llmd spelling
// ("llmd"/"llm-d", case-insensitive) selects the server-sim backend, so the
// default and any unknown value preserve today's behavior.
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ctrl.BackendEnvName))) {
	case "llmd", "llm-d":
		return ModeLLMD
	default:
		return ModeServerSim
	}
}

// Sensor reads one managed Deployment into a deployment-level ServerSpec plus
// one ServerSpec per reporting replica. A non-nil err signals an operational
// failure and the caller drops the deployment for the cycle; on success an empty
// replicas slice with a zeroed server spec is a valid "nothing reporting yet"
// reading (numReplicas/labels still flow).
type Sensor interface {
	Sense(ctx context.Context, dep appsv1.Deployment, kc kubernetes.Interface) (
		server config.ServerSpec, replicas []config.ServerSpec, err error)
}

// DeploymentUpdate is the resolved patch target for a single managed server.
type DeploymentUpdate struct {
	ServerName string
	DeployName string
	Namespace  string
	Allocation config.AllocationData
}

// Actuator applies one resolved allocation onto one managed Deployment.
type Actuator interface {
	Actuate(ctx context.Context, kc kubernetes.Interface, u DeploymentUpdate) error
}
