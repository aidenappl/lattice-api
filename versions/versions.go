package versions

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	pollInterval = 30 * time.Minute
	httpTimeout  = 10 * time.Second
)

// Repos to check for latest releases.
var repos = []string{
	"aidenappl/lattice-api",
	"aidenappl/lattice-web",
	"aidenappl/lattice-runner",
}

// githubRelease is the subset of the GitHub releases API response we care about.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// Cache holds the latest release tag for each repo.
type Cache struct {
	mu      sync.RWMutex
	latest  map[string]string // repo -> tag_name
	checked time.Time
}

var cache = &Cache{
	latest: make(map[string]string),
}

// Start begins background polling for latest GitHub releases.
func Start() {
	// Initial fetch (non-blocking — runs in background).
	go func() {
		Refresh()
		ticker := time.NewTicker(pollInterval)
		for range ticker.C {
			Refresh()
		}
	}()
}

// Refresh fetches the latest release for all repos and updates the cache.
func Refresh() {
	for _, repo := range repos {
		tag, err := fetchLatestRelease(repo)
		if err != nil {
			log.Printf("versions: failed to fetch latest release for %s: %v", repo, err)
			continue
		}
		cache.mu.Lock()
		cache.latest[repo] = tag
		cache.mu.Unlock()
	}
	cache.mu.Lock()
	cache.checked = time.Now()
	cache.mu.Unlock()
	log.Printf("versions: refreshed — api=%s web=%s runner=%s",
		Get("aidenappl/lattice-api"),
		Get("aidenappl/lattice-web"),
		Get("aidenappl/lattice-runner"),
	)
}

// Get returns the cached latest release tag for a repo, or "" if unknown.
func Get(repo string) string {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.latest[repo]
}

// LastChecked returns when the cache was last refreshed.
func LastChecked() time.Time {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.checked
}

// LatestAPI returns the latest release tag for lattice-api.
func LatestAPI() string { return Get("aidenappl/lattice-api") }

// LatestWeb returns the latest release tag for lattice-web.
func LatestWeb() string { return Get("aidenappl/lattice-web") }

// LatestRunner returns the latest release tag for lattice-runner.
func LatestRunner() string { return Get("aidenappl/lattice-runner") }

func fetchLatestRelease(repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	client := &http.Client{Timeout: httpTimeout}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lattice-api")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("no releases found for %s", repo)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d for %s", resp.StatusCode, repo)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("empty tag_name in release for %s", repo)
	}

	return release.TagName, nil
}
