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
	pollInterval  = 30 * time.Minute
	httpTimeout   = 10 * time.Second
	defaultBranch = "main"
)

// Repos to check for latest commits.
var repos = []string{
	"aidenappl/lattice-api",
	"aidenappl/lattice-web",
	"aidenappl/lattice-runner",
}

// githubCommit is the subset of the GitHub commits API response we care about.
type githubCommit struct {
	SHA string `json:"sha"`
}

// Cache holds the latest commit short SHA for each repo.
type Cache struct {
	mu      sync.RWMutex
	latest  map[string]string // repo -> short SHA
	checked time.Time
}

var cache = &Cache{
	latest: make(map[string]string),
}

// Start begins background polling for latest GitHub commits.
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

// Refresh fetches the latest commit short SHA for all repos and updates the cache.
func Refresh() {
	for _, repo := range repos {
		sha, err := fetchLatestCommitSHA(repo)
		if err != nil {
			log.Printf("versions: failed to fetch latest commit for %s: %v", repo, err)
			continue
		}
		cache.mu.Lock()
		cache.latest[repo] = sha
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

// Get returns the cached latest short SHA for a repo, or "" if unknown.
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

// LatestAPI returns the latest short SHA for lattice-api.
func LatestAPI() string { return Get("aidenappl/lattice-api") }

// LatestWeb returns the latest short SHA for lattice-web.
func LatestWeb() string { return Get("aidenappl/lattice-web") }

// LatestRunner returns the latest short SHA for lattice-runner.
func LatestRunner() string { return Get("aidenappl/lattice-runner") }

// fetchLatestCommitSHA returns the short (7-char) SHA of the most recent commit
// on the default branch of the given repo. This matches the format used in the
// APIVersion / runner_version fields baked in via ldflags at build time.
func fetchLatestCommitSHA(repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits?per_page=1&sha=%s", repo, defaultBranch)
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
		return "", fmt.Errorf("repo not found or no commits: %s", repo)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d for %s", resp.StatusCode, repo)
	}

	var commits []githubCommit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(commits) == 0 || commits[0].SHA == "" {
		return "", fmt.Errorf("no commits found for %s", repo)
	}

	// Truncate to 7 chars to match `git rev-parse --short HEAD`.
	sha := commits[0].SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return sha, nil
}
