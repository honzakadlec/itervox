// Package jira implements the tracker.Tracker interface against the Jira
// Cloud REST API (v3), authenticating with HTTP Basic auth (account email +
// API token). Jira Server/Data Center is not supported.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

const apiPathV3 = "/rest/api/3"
const httpTimeout = 30 * time.Second

// ClientConfig holds configuration for the Jira Cloud REST tracker adapter.
type ClientConfig struct {
	// APIKey is the Atlassian API token, paired with Username for HTTP Basic auth.
	APIKey string
	// Username is the Atlassian account email paired with APIKey.
	Username string
	// Endpoint is the Jira Cloud site base URL (e.g. "https://yourdomain.atlassian.net").
	Endpoint string
	// ProjectSlug is the Jira project key (e.g. "PROJ").
	ProjectSlug string
	// ActiveStates lists Jira workflow status names treated as active/candidate.
	ActiveStates []string
	// TerminalStates lists Jira workflow status names treated as terminal.
	TerminalStates []string
	// BacklogStates lists Jira workflow status names always fetched for the backlog column.
	BacklogStates []string
	// DefaultIssueType names the issue type used by CreateIssue. Defaults to "Task".
	DefaultIssueType string
}

// Client is the Jira Cloud REST tracker adapter.
type Client struct {
	cfg        ClientConfig
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a new Jira Client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.DefaultIssueType == "" {
		cfg.DefaultIssueType = "Task"
	}
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: httpTimeout},
		baseURL:    strings.TrimSuffix(cfg.Endpoint, "/") + apiPathV3,
	}
}

var _ tracker.Tracker = (*Client)(nil)

func (c *Client) authHeader() string {
	token := base64.StdEncoding.EncodeToString([]byte(c.cfg.Username + ":" + c.cfg.APIKey))
	return "Basic " + token
}

// doJSON performs an HTTP request against the Jira REST API and decodes a
// JSON object response. reqBody, when non-nil, is marshaled as the request
// body. A nil response map with a nil error means the response body was empty.
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any) (map[string]any, error) {
	var reader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("jira: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("jira: build request: %w", err)
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jira: read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &tracker.NotFoundError{Adapter: "jira"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tracker.APIStatusError{Adapter: "jira", Status: resp.StatusCode}
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("jira: decode response: %w", err)
	}
	return out, nil
}
