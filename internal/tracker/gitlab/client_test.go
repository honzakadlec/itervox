package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

// newTestClient builds a Client pointed at ts, bypassing the hardcoded
// gitlab.com base URL (there is no Endpoint config field for this adapter —
// see ClientConfig doc — so tests construct the Client directly instead of
// going through NewClient).
func newTestClient(baseURL string, cfg ClientConfig) *Client {
	return &Client{
		cfg:         cfg,
		httpClient:  &http.Client{},
		baseURL:     baseURL,
		projectPath: url.PathEscape(cfg.ProjectSlug),
	}
}

func defaultTestConfig() ClientConfig {
	return ClientConfig{
		APIKey:          "glpat-test",
		ProjectSlug:     "group/project",
		ActiveStates:    []string{"todo", "in-progress"},
		TerminalStates:  []string{"done", "cancelled"},
		CompletionState: "done",
	}
}

func glIssue(iid int, title, state string, labels []string) map[string]any {
	labelObjs := make([]any, len(labels))
	for i, l := range labels {
		labelObjs[i] = l
	}
	return map[string]any{
		"iid":         float64(iid),
		"title":       title,
		"state":       state,
		"labels":      labelObjs,
		"web_url":     fmt.Sprintf("https://gitlab.com/group/project/-/issues/%d", iid),
		"description": "",
		"created_at":  "2024-01-01T00:00:00Z",
		"updated_at":  "2024-01-01T00:00:00Z",
	}
}

func TestNewClientDefaultsToGitLabComBaseURL(t *testing.T) {
	c := NewClient(ClientConfig{ProjectSlug: "group/project"})
	assert.Equal(t, "https://gitlab.com/api/v4", c.baseURL)
	assert.Equal(t, "group%2Fproject", c.projectPath)
}

func TestFetchCandidateIssuesOneRequestPerActiveState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("labels") {
		case "status::todo":
			_ = json.NewEncoder(w).Encode([]any{glIssue(1, "Fix bug", "opened", []string{"status::todo"})})
		case "status::in-progress":
			_ = json.NewEncoder(w).Encode([]any{glIssue(2, "Add feature", "opened", []string{"status::in-progress"})})
		default:
			t.Errorf("unexpected labels filter %q", r.URL.Query().Get("labels"))
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "#1", issues[0].Identifier)
	assert.Equal(t, "#2", issues[1].Identifier)
}

func TestFetchCandidateIssuesPaginatedLinkHeader(t *testing.T) {
	mux := http.NewServeMux()
	calls := 0
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") != "status::todo" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode([]any{glIssue(2, "Issue 2", "opened", []string{"status::todo"})})
			return
		}
		calls++
		nextURL := fmt.Sprintf("http://%s/projects/group%%2Fproject/issues?labels=status::todo&page=2", r.Host)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		_ = json.NewEncoder(w).Encode([]any{glIssue(1, "Issue 1", "opened", []string{"status::todo"})})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := defaultTestConfig()
	cfg.ActiveStates = []string{"todo"}
	client := newTestClient(ts.URL, cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, 1, calls)
}

func TestFetchIssuesByStatesEmptyReturnsEmpty(t *testing.T) {
	client := newTestClient("http://should-not-be-called.invalid", defaultTestConfig())
	result, err := client.FetchIssuesByStates(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFetchIssuesByStatesUsesStateAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "all", r.URL.Query().Get("state"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{glIssue(5, "Done issue", "closed", []string{"status::done"})})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	result, err := client.FetchIssuesByStates(context.Background(), []string{"done"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "done", result[0].State)
}

func TestFetchIssueStatesByIDsFanOut(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(1, "Issue 1", "opened", []string{"status::in-progress"}))
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(2, "Issue 2", "opened", []string{"status::todo"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issues, err := client.FetchIssueStatesByIDs(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

func TestFetchIssueStatesByIDsSkipsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(1, "Issue 1", "opened", []string{"status::todo"}))
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issues, err := client.FetchIssueStatesByIDs(context.Background(), []string{"1", "99"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "#1", issues[0].Identifier)
}

func TestFetchCandidateIssuesPopulatesBlockerStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "status::todo" {
			issue := glIssue(3, "Blocked issue", "opened", []string{"status::todo"})
			issue["description"] = "Blocked by #10"
			_ = json.NewEncoder(w).Encode([]any{issue})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(10, "Blocker 10", "closed", []string{"status::done"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := defaultTestConfig()
	cfg.ActiveStates = []string{"todo"}
	client := newTestClient(ts.URL, cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	require.NotNil(t, issues[0].BlockedBy[0].State)
	assert.Equal(t, "done", *issues[0].BlockedBy[0].State)
}

func TestFetchCandidateIssuesParsesBlockedByHeadingSection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "status::todo" {
			issue := glIssue(14, "Blocked issue", "opened", []string{"status::todo"})
			issue["description"] = "## What to build\n\nStuff.\n\n## Blocked by\n\n- #13\n\n<details>notes</details>\n"
			_ = json.NewEncoder(w).Encode([]any{issue})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/13", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(13, "Blocker 13", "closed", []string{"status::done"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := defaultTestConfig()
	cfg.ActiveStates = []string{"todo"}
	client := newTestClient(ts.URL, cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	assert.Equal(t, "#13", *issues[0].BlockedBy[0].Identifier)
	require.NotNil(t, issues[0].BlockedBy[0].State)
	assert.Equal(t, "done", *issues[0].BlockedBy[0].State)
}

func TestGetReturnsNotFoundError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	_, err := client.FetchIssueDetail(context.Background(), "1")
	require.Error(t, err)
	var nfe *tracker.NotFoundError
	assert.True(t, errors.As(err, &nfe))
	assert.True(t, errors.Is(err, tracker.ErrNotFound))
}

func TestGetReturnsAPIStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	_, err := client.FetchIssueDetail(context.Background(), "1")
	require.Error(t, err)
	var apiErr *tracker.APIStatusError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusInternalServerError, apiErr.Status)
}

func TestRateLimitSnapshotCapturedFromHeaders(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "2000")
		w.Header().Set("RateLimit-Remaining", "1999")
		w.Header().Set("RateLimit-Reset", "1700000000")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(1, "Issue 1", "opened", []string{"status::todo"}))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	_, err := client.FetchIssueDetail(context.Background(), "1")
	require.NoError(t, err)

	snap := client.RateLimitSnapshot()
	require.NotNil(t, snap)
	assert.Equal(t, 2000, snap.RequestsLimit)
	assert.Equal(t, 1999, snap.RequestsRemaining)
	require.NotNil(t, snap.Reset)
}

func TestRateLimitSnapshotNilWhenNoHeadersSeen(t *testing.T) {
	client := newTestClient("http://unused.invalid", defaultTestConfig())
	assert.Nil(t, client.RateLimitSnapshot())
}

func TestParseNextLinkEmptyHeaderMeansDone(t *testing.T) {
	next, err := ParseNextLink("")
	require.NoError(t, err)
	assert.Empty(t, next)
}

func TestParseNextLinkMissingRelNextErrors(t *testing.T) {
	_, err := ParseNextLink(`<https://gitlab.com/api/v4/x>; rel="prev"`)
	assert.ErrorIs(t, err, ErrMissingPageLink)
}

func TestParseNextLinkExtractsURL(t *testing.T) {
	next, err := ParseNextLink(`<https://gitlab.com/api/v4/projects/1/issues?page=2>; rel="next"`)
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.com/api/v4/projects/1/issues?page=2", next)
}
