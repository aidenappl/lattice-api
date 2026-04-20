package routers

import (
	"log"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"gopkg.in/yaml.v3"
)

// BackfillNetworksFromCompose parses stored compose YAML for all stacks
// and creates network records for any stack that has compose-defined networks
// but no entries in the networks table. Runs once at startup.
func BackfillNetworksFromCompose(engine db.Queryable) {
	stacks, err := query.ListStacks(engine, query.ListStacksRequest{Limit: 10000})
	if err != nil || stacks == nil {
		return
	}

	for _, stack := range *stacks {
		if stack.ComposeYAML == nil || *stack.ComposeYAML == "" {
			continue
		}

		// Check if this stack already has networks
		existing, err := query.ListNetworksByStack(engine, stack.ID)
		if err == nil && existing != nil && len(*existing) > 0 {
			continue
		}

		// Parse compose YAML for networks
		var compose composeFile
		if err := yaml.Unmarshal([]byte(*stack.ComposeYAML), &compose); err != nil {
			continue
		}

		if len(compose.Networks) == 0 {
			continue
		}

		for key, net := range compose.Networks {
			driver := net.Driver
			if driver == "" {
				driver = "bridge"
			}
			name := net.Name
			if name == "" {
				name = key
			}
			if err := query.CreateNetwork(engine, query.CreateNetworkRequest{
				StackID: stack.ID,
				Name:    name,
				Driver:  driver,
			}); err != nil {
				log.Printf("backfill: failed to create network %q for stack %d: %v", name, stack.ID, err)
			}
		}

		log.Printf("backfill: created %d network(s) for stack %q (id=%d)", len(compose.Networks), stack.Name, stack.ID)
	}
}
