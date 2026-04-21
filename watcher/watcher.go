package watcher

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/registry"
	"github.com/aidenappl/lattice-api/webhooks"
)

// lastKnownDigests stores the latest known manifest digest per image reference
// to detect when a tag has been re-pushed to a new image.
var (
	lastKnownDigests = make(map[string]string)
	mu               sync.Mutex
)

// Start launches a background goroutine that polls registries for image changes.
// Checks every 5 minutes.
func Start() {
	go func() {
		// Initial delay to let the app fully start and DB connections stabilise.
		time.Sleep(2 * time.Minute)
		poll()

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			poll()
		}
	}()
	logger.Info("watcher", "image version watcher started (first poll in 2 minutes)")
}

func poll() {
	// Get all active stacks
	stacks, err := query.ListStacks(db.DB, query.ListStacksRequest{Limit: 500})
	if err != nil || stacks == nil {
		return
	}

	// Get all registries (with decrypted credentials) for auth lookups
	registries, _ := query.ListRegistries(db.DB)

	// Build a map of registry ID -> Registry for quick lookup
	regMap := make(map[int]*registry.Client)
	regNames := make(map[int]string)
	if registries != nil {
		for _, r := range *registries {
			username := ""
			password := ""
			if r.Username != nil {
				username = *r.Username
			}
			if r.Password != nil {
				password = *r.Password
			}
			regMap[r.ID] = registry.NewClient(r.URL, username, password)
			regNames[r.ID] = r.URL
		}
	}

	for _, stack := range *stacks {
		containers, err := query.ListContainersByStack(db.DB, stack.ID)
		if err != nil || containers == nil {
			continue
		}

		for _, c := range *containers {
			if c.RegistryID == nil {
				continue
			}

			reg, ok := regMap[*c.RegistryID]
			if !ok {
				continue
			}

			repo := c.Image
			currentTag := c.Tag
			if currentTag == "" {
				currentTag = "latest"
			}

			cacheKey := repo + ":" + currentTag

			// Try to get the manifest digest for this tag. This is a HEAD request
			// and returns the Docker-Content-Digest, which changes when the tag
			// is re-pushed to a new image.
			digest, err := reg.GetManifestDigest(repo, currentTag)
			if err != nil {
				// Fallback: use the tag list as a fingerprint. This catches new
				// tags being pushed but won't detect mutable tag re-pushes.
				tags, tagErr := reg.ListTags(repo)
				if tagErr != nil {
					continue
				}
				sort.Strings(tags)
				digest = "taglist:" + strings.Join(tags, ",")
			}

			mu.Lock()
			prev, exists := lastKnownDigests[cacheKey]
			if exists && prev != digest {
				// Image changed
				logger.Info("watcher", "image change detected", logger.F{"image": cacheKey, "stack": stack.Name})

				webhooks.Fire("image.updated", map[string]any{
					"stack_id":   stack.ID,
					"stack_name": stack.Name,
					"container":  c.Name,
					"image":      repo,
					"tag":        currentTag,
				})

				if stack.AutoDeploy {
					logger.Info("watcher", "auto-deploy requested", logger.F{"stack": stack.Name})
					webhooks.Fire("image.auto_deploy_requested", map[string]any{
						"stack_id":   stack.ID,
						"stack_name": stack.Name,
						"container":  c.Name,
						"image":      repo,
						"tag":        currentTag,
						"message":    "New image version detected. Use a deploy token to trigger deployment.",
					})
				}
			}
			lastKnownDigests[cacheKey] = digest
			mu.Unlock()
		}
	}
}
