package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/tools"
)

// escapeRepoPath percent-escapes each path segment of a repository name while
// preserving the "/" separators that registry repository paths legitimately
// contain (e.g. "library/nginx"). This prevents path/URL injection via a
// user-supplied repository or tag.
func escapeRepoPath(repo string) string {
	parts := strings.Split(repo, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Client communicates with a Docker Registry HTTP API v2.
type Client struct {
	URL      string // e.g. "https://registry.appleby.cloud"
	Username string
	Password string
	client   *http.Client
}

func NewClient(url, username, password string) *Client {
	url = strings.TrimRight(url, "/")
	return &Client{
		URL:      url,
		Username: username,
		Password: password,
		client: &http.Client{
			Timeout: 10 * time.Second,
			// Reject redirects that would send the request (and its Basic-auth
			// credentials) to a private/reserved or non-HTTPS host — an SSRF
			// vector where a malicious registry 302s us at an internal service.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				if err := tools.ValidateExternalURL(req.URL.String()); err != nil {
					return fmt.Errorf("blocked redirect to %s: %w", req.URL.Host, err)
				}
				return nil
			},
		},
	}
}

// Ping checks connectivity and authentication against the registry.
// Returns nil if the registry is reachable and credentials are valid.
func (c *Client) Ping() error {
	req, err := http.NewRequest("GET", c.URL+"/v2/", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("registry unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed (401)")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("access denied (403)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

// CatalogResponse is the response from GET /v2/_catalog.
type CatalogResponse struct {
	Repositories []string `json:"repositories"`
}

// ListRepositories returns all repository names in the registry.
func (c *Client) ListRepositories() ([]string, error) {
	req, err := http.NewRequest("GET", c.URL+"/v2/_catalog", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed (401)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var catalog CatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("failed to decode catalog: %w", err)
	}

	return catalog.Repositories, nil
}

// TagsResponse is the response from GET /v2/{name}/tags/list.
type TagsResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// ListTags returns all tags for a given repository.
func (c *Client) ListTags(repository string) ([]string, error) {
	req, err := http.NewRequest("GET", c.URL+"/v2/"+escapeRepoPath(repository)+"/tags/list", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed (401)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("repository not found: %s", repository)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var tags TagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("failed to decode tags: %w", err)
	}

	return tags.Tags, nil
}

// GetManifestDigest returns the Docker-Content-Digest for a given repository:tag
// without downloading the full manifest. This is used to detect when a mutable
// tag (e.g. "latest") has been re-pushed to a new image.
func (c *Client) GetManifestDigest(repository, tag string) (string, error) {
	req, err := http.NewRequest("HEAD", c.URL+"/v2/"+escapeRepoPath(repository)+"/manifests/"+url.PathEscape(tag), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuth(req)
	// Accept both OCI and Docker manifest types so the registry returns a digest.
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("authentication failed (401)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("manifest not found: %s:%s", repository, tag)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry did not return Docker-Content-Digest header")
	}
	return digest, nil
}

func (c *Client) setAuth(req *http.Request) {
	if c.Username != "" && c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
}
