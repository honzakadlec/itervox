// Package gitlab implements the tracker.Tracker interface against the
// GitLab REST API v4, authenticating with a Personal Access Token. Only
// gitlab.com is supported (no self-hosted instances).
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

const defaultBaseURL = "https://gitlab.com/api/v4"
const httpTimeout = 30 * time.Second
const pageSize = 50

// statusLabelPrefix is the hardcoded scoped-label prefix used to map itervox
// state names onto GitLab labels (e.g. "in-progress" -> "status::in-progress").
// GitLab enforces at most one label per scope on an issue, so assigning a new
// status::* label automatically clears any previous one.
const statusLabelPrefix = "status::"

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ErrMissingPageLink is returned when a non-empty Link header contains no rel="next" entry.
var ErrMissingPageLink = errors.New("gitlab_missing_page_link")

// ClientConfig holds configuration for the GitLab REST tracker adapter.
type ClientConfig struct {
	// APIKey is a GitLab Personal Access Token (scopes: api).
	APIKey string
	// ProjectSlug is the GitLab project path (e.g. "group/subgroup/project").
	ProjectSlug string
	// ActiveStates and TerminalStates are itervox state names, mapped onto
	// scoped labels as "status::<name>" (case preserved, matched case-insensitively).
	ActiveStates   []string
	TerminalStates []string
	BacklogStates  []string
	// CompletionState, when non-empty, is the state name that also closes the
	// GitLab issue natively (state_event: close) in addition to setting the
	// status::<CompletionState> label. Other terminal states (e.g. a failed
	// state) only change the label and leave the issue open.
	CompletionState string
	// ReviewState, when non-empty, is a non-active, non-terminal state
	// (status::<ReviewState>) for issues whose agent work is done but not yet
	// merged. It only changes the label; the issue stays open.
	ReviewState string
}

// rateLimitSnapshot holds the most recent RateLimit-* values observed.
type rateLimitSnapshot struct {
	limit     int
	remaining int
	reset     *time.Time
}

// Client is the GitLab REST tracker adapter.
type Client struct {
	cfg           ClientConfig
	httpClient    *http.Client
	baseURL       string
	projectPath   string // URL-encoded ProjectSlug, ready to splice into API paths
	rateMu        sync.RWMutex
	lastRateLimit *rateLimitSnapshot
}

// NewClient creates a new GitLab Client.
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: httpTimeout},
		baseURL:     defaultBaseURL,
		projectPath: url.PathEscape(cfg.ProjectSlug),
	}
}

var _ tracker.Tracker = (*Client)(nil)
var _ tracker.RateLimiter = (*Client)(nil)

// projectURL builds a URL under /projects/:id for the configured project.
func (c *Client) projectURL(pathSuffix string) string {
	return fmt.Sprintf("%s/projects/%s%s", c.baseURL, c.projectPath, pathSuffix)
}

// statusLabel maps an itervox state name onto its scoped GitLab label.
func statusLabel(stateName string) string {
	return statusLabelPrefix + stateName
}

// get performs an authenticated GET request and returns the decoded JSON body
// (either a map or a slice, depending on the endpoint) along with the response's
// Link header for pagination.
func (c *Client) get(ctx context.Context, requestURL string) (any, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("gitlab_api_request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.cfg.APIKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("gitlab_api_request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.snapshotRateLimit(resp)

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", &tracker.NotFoundError{Adapter: "gitlab"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &tracker.APIStatusError{Adapter: "gitlab", Status: resp.StatusCode}
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("gitlab_api_request: read body: %w", err)
	}
	var result any
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return nil, "", fmt.Errorf("gitlab_api_request: decode json: %w", err)
	}
	return result, resp.Header.Get("Link"), nil
}

// doJSON performs an authenticated request with a JSON body (POST/PUT) and
// decodes the JSON object response.
func (c *Client) doJSON(ctx context.Context, method, requestURL string, reqBody any) (map[string]any, error) {
	var reader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("gitlab: encode request: %w", err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.cfg.APIKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.snapshotRateLimit(resp)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gitlab: read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, &tracker.NotFoundError{Adapter: "gitlab"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tracker.APIStatusError{Adapter: "gitlab", Status: resp.StatusCode}
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("gitlab: decode response: %w", err)
	}
	return out, nil
}

// snapshotRateLimit captures RateLimit-* headers from any response.
func (c *Client) snapshotRateLimit(resp *http.Response) {
	limitStr := resp.Header.Get("RateLimit-Limit")
	if limitStr == "" {
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		return
	}
	remaining, _ := strconv.Atoi(resp.Header.Get("RateLimit-Remaining"))
	var reset *time.Time
	if ts, err := strconv.ParseInt(resp.Header.Get("RateLimit-Reset"), 10, 64); err == nil {
		t := time.Unix(ts, 0)
		reset = &t
	}
	c.rateMu.Lock()
	c.lastRateLimit = &rateLimitSnapshot{limit: limit, remaining: remaining, reset: reset}
	c.rateMu.Unlock()
}

// RateLimitSnapshot implements tracker.RateLimiter so callers can type-assert
// Tracker to tracker.RateLimiter without importing this concrete package.
func (c *Client) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	c.rateMu.RLock()
	defer c.rateMu.RUnlock()
	if c.lastRateLimit == nil {
		return nil
	}
	return &tracker.RateLimitSnapshot{
		RequestsLimit:     c.lastRateLimit.limit,
		RequestsRemaining: c.lastRateLimit.remaining,
		Reset:             c.lastRateLimit.reset,
	}
}

// ParseNextLink extracts the "next" URL from a GitLab Link header.
// Returns ("", nil) when header is empty (no more pages).
// Returns ("", ErrMissingPageLink) when header is non-empty but has no rel="next".
func ParseNextLink(linkHeader string) (string, error) {
	if linkHeader == "" {
		return "", nil
	}
	m := linkNextRe.FindStringSubmatch(linkHeader)
	if m == nil {
		return "", ErrMissingPageLink
	}
	return m[1], nil
}
