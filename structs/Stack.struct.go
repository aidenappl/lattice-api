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
	SecretEnvKeys        *string `json:"secret_env_keys"`
	ComposeYAML          *string `json:"compose_yaml"`
	PlacementConstraints *string `json:"placement_constraints"`
	// ImagePruneKeep is how many previous images per repository the runner
	// keeps when it prunes after a successful deploy; IMAGE_PRUNE_DISABLED
	// turns the prune off.
	ImagePruneKeep int       `json:"image_prune_keep"`
	Active         bool      `json:"active"`
	UpdatedAt      time.Time `json:"updated_at"`
	InsertedAt     time.Time `json:"inserted_at"`
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

// Post-deploy image prune bounds for stacks.image_prune_keep (APL-86).
const (
	IMAGE_PRUNE_DEFAULT_KEEP = 2
	IMAGE_PRUNE_DISABLED     = -1
	IMAGE_PRUNE_MAX_KEEP     = 20
)

// ValidImagePruneKeep reports whether keep is IMAGE_PRUNE_DISABLED or 0..IMAGE_PRUNE_MAX_KEEP.
func ValidImagePruneKeep(keep int) bool {
	return keep == IMAGE_PRUNE_DISABLED || (keep >= 0 && keep <= IMAGE_PRUNE_MAX_KEEP)
}

// INVALID_IMAGE_PRUNE_KEEP is the 400 message for a value ValidImagePruneKeep rejects.
const INVALID_IMAGE_PRUNE_KEEP = "image_prune_keep must be -1 (disabled) or between 0 and 20"

// ImagePrunePayload is the deploy spec's image_prune block for a stack.
func ImagePrunePayload(keep int) map[string]any {
	if keep == IMAGE_PRUNE_DISABLED {
		return map[string]any{"enabled": false, "keep": 0}
	}
	return map[string]any{"enabled": true, "keep": keep}
}
