package structs

import "time"

type Stack struct {
	ID                 int     `json:"id"`
	Name               string  `json:"name"`
	Description        *string `json:"description"`
	WorkerID           *int    `json:"worker_id"`
	Status             string  `json:"status"`
	DeploymentStrategy string  `json:"deployment_strategy"`
	AutoDeploy         bool    `json:"auto_deploy"`
	EnvVars            *string `json:"env_vars"`
	// SecretEnvKeys is a JSON array naming the env_vars keys whose values are
	// masked ("***") for non-admins in every API response.
	SecretEnvKeys        *string   `json:"secret_env_keys"`
	ComposeYAML          *string   `json:"compose_yaml"`
	PlacementConstraints *string   `json:"placement_constraints"`
	Active               bool      `json:"active"`
	UpdatedAt            time.Time `json:"updated_at"`
	InsertedAt           time.Time `json:"inserted_at"`
}

// DeploymentStrategy is a stack's (and a deployment's) rollout strategy. The
// columns are ENUM('rolling', 'blue-green', 'canary') (001_initial.sql), so a
// value outside this set reached MariaDB and came back as Error 1265 — a 500
// for what is the caller's mistake. Handlers check IsValid and answer 400.
type DeploymentStrategy string

const (
	DeploymentStrategyRolling   DeploymentStrategy = "rolling"
	DeploymentStrategyBlueGreen DeploymentStrategy = "blue-green"
	DeploymentStrategyCanary    DeploymentStrategy = "canary"
)

func (s DeploymentStrategy) IsValid() bool {
	switch s {
	case DeploymentStrategyRolling, DeploymentStrategyBlueGreen, DeploymentStrategyCanary:
		return true
	}
	return false
}

// INVALID_DEPLOYMENT_STRATEGY is the 400 message for a strategy IsValid rejects.
const INVALID_DEPLOYMENT_STRATEGY = "deployment_strategy must be one of: rolling, blue-green, canary"
