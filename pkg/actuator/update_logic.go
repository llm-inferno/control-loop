package actuator

import (
	"sort"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"

	"github.com/llm-inferno/control-loop/pkg/backend"
	"github.com/llm-inferno/optimizer-light/pkg/config"
)

// ComputeUpdates produces one DeploymentUpdate per serverMap entry, applying
// the optimizer's allocation when present and the zero allocation otherwise.
//
// The set of updates is exactly serverMap (the Collector's view); allocations
// for server names not in serverMap are dropped because the Actuator has no
// Kube reference for them. Output is sorted by ServerName for stable logging.
func ComputeUpdates(
	allocMap map[string]config.AllocationData,
	serverMap map[string]ctrl.ServerKubeInfo,
) []backend.DeploymentUpdate {
	names := make([]string, 0, len(serverMap))
	for name := range serverMap {
		names = append(names, name)
	}
	sort.Strings(names)

	updates := make([]backend.DeploymentUpdate, 0, len(names))
	for _, name := range names {
		info := serverMap[name]
		alloc, ok := allocMap[name]
		if !ok {
			alloc = config.AllocationData{} // zero value: replicas=0, accelerator="", load=0
		}
		updates = append(updates, backend.DeploymentUpdate{
			ServerName: name,
			DeployName: info.Name,
			Namespace:  info.Space,
			Allocation: alloc,
		})
	}
	return updates
}
