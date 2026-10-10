package versions

import (
	"bufio"
	"context"
	"fmt"
	"github.com/aidenappl/lattice-api/logger"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	monitor "github.com/aidenappl/go-monitor"
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
		safeRefresh()
		ticker := time.NewTicker(pollInterval)
		for range ticker.C {
			safeRefresh()
		}
	}()
}

// safeRefresh is one poll that cannot end the loop.
func safeRefresh() {
	defer logger.Recover("versions.refresh")
	Refresh()
}

// Refresh fetches the latest release for all repos and updates the cache.
func Refresh() {
	ctx := monitor.WithJobID(context.Background(), monitor.NewJobID())
	for _, repo := range repos {
		tag, err := fetchLatestTag(repo)
		if err != nil {
			logger.WarnCtx(ctx, "versions", "failed to fetch latest release", logger.F{"repo": repo, "error": err})
			continue
		}
		cache.mu.Lock()
		cache.latest[repo] = tag
		cache.mu.Unlock()
	}
	cache.mu.Lock()
	cache.checked = time.Now()
	cache.mu.Unlock()
	logger.InfoCtx(ctx, "versions", "latest releases refreshed", logger.F{
		"api":    Get("aidenappl/lattice-api"),
		"web":    Get("aidenappl/lattice-web"),
		"runner": Get("aidenappl/lattice-runner"),
	})
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

// fetchLatestTag returns the highest vX.Y.Z tag of a public GitHub repo. It
// reads the git smart-HTTP ref advertisement (what `git ls-remote` uses) rather
// than api.github.com: the REST API allows 60 unauthenticated calls per hour per
// IP, which this control plane shares with the workers, and a 403 left the
// "latest" versions blank. Every CI release pushes a vX.Y.Z tag, so the newest
// tag is the newest release.
func fetchLatestTag(repo string) (string, error) {
	url := fmt.Sprintf("https://github.com/%s.git/info/refs?service=git-upload-pack", repo)
	client := &http.Client{Timeout: httpTimeout}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "lattice-api")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub returned %d for %s refs", resp.StatusCode, repo)
	}

	tag := latestSemverTag(io.LimitReader(resp.Body, 4<<20))
	if tag == "" {
		return "", fmt.Errorf("no vX.Y.Z tags found for %s", repo)
	}
	return tag, nil
}

// latestSemverTag scans a ref advertisement for refs/tags/vX.Y.Z and returns the
// highest by numeric comparison. Peeled entries (^{}) and other tags are ignored.
func latestSemverTag(r io.Reader) string {
	best, bestV := "", [3]int{-1, -1, -1}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		_, ref, ok := strings.Cut(sc.Text(), " refs/tags/")
		if !ok {
			continue
		}
		ref, _, _ = strings.Cut(ref, "\x00")
		v, ok := parseSemver(ref)
		if ok && (v[0] > bestV[0] || v[0] == bestV[0] && (v[1] > bestV[1] || v[1] == bestV[1] && v[2] > bestV[2])) {
			best, bestV = ref, v
		}
	}
	return best
}

// parseSemver parses exactly vMAJOR.MINOR.PATCH.
func parseSemver(tag string) ([3]int, bool) {
	var v [3]int
	rest, ok := strings.CutPrefix(tag, "v")
	if !ok {
		return v, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
