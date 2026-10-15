package routers

import (
	"fmt"
	"sort"

	"github.com/aidenappl/lattice-api/query"
	"gopkg.in/yaml.v3"
)

// composeNetworkRequests turns the top-level networks of a compose file into
// network records for a stack, using the same defaults as a compose import
// (driver bridge, name = key unless `name:` is set). Sorted by name so the
// result is deterministic.
func composeNetworkRequests(stackID int, composeYAML string) ([]query.CreateNetworkRequest, error) {
	if composeYAML == "" {
		return nil, nil
	}
	var compose composeFile
	if err := yaml.Unmarshal([]byte(composeYAML), &compose); err != nil {
		return nil, fmt.Errorf("invalid compose yaml: %w", err)
	}
	out := make([]query.CreateNetworkRequest, 0, len(compose.Networks))
	for key, net := range compose.Networks {
		driver := net.Driver
		if driver == "" {
			driver = "bridge"
		}
		name := net.Name
		if name == "" {
			name = key
		}
		out = append(out, query.CreateNetworkRequest{StackID: stackID, Name: name, Driver: driver})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
