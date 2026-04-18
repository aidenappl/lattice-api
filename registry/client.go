package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	req, err := http.NewRequest("GET", c.URL+"/v2/"+repository+"/tags/list", nil)
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

func (c *Client) setAuth(req *http.Request) {
	if c.Username != "" && c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
}
