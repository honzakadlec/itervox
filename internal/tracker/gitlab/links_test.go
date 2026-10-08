package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
)

// glLink builds an entry of GET /projects/:id/issues/:iid/links. GitLab
// returns the linked issue's fields plus link_type, relative to the issue
// being queried (so the blocked issue sees "is_blocked_by").
func glLink(iid int, state string, labels []string, linkType, relativeRef, fullRef string) map[string]any {
	link := glIssue(iid, fmt.Sprintf("Linked %d", iid), state, labels)
	link["link_type"] = linkType
	link["references"] = map[string]any{
		"short":    fmt.Sprintf("#%d", iid),
		"relative": relativeRef,
		"full":     fullRef,
	}
	return link
}

// linksTestServer serves one status::todo candidate (#3, with the given
// description) and the given /issues/3/links handler.
func linksTestServer(t *testing.T, description string, links http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "status::todo" {
			issue := glIssue(3, "Blocked issue", "opened", []string{"status::todo"})
			issue["description"] = description
			_ = json.NewEncoder(w).Encode([]any{issue})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/3/links", links)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func encodeLinks(links ...map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out := make([]any, len(links))
		for i, l := range links {
			out[i] = l
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

func fetchTodoCandidate(t *testing.T, ts *httptest.Server) domain.Issue {
	t.Helper()
	cfg := defaultTestConfig()
	cfg.ActiveStates = []string{"todo"}
	client := newTestClient(ts.URL, cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	return issues[0]
}

func TestNativeLinkOpenBlockerIsNonTerminal(t *testing.T) {
	ts := linksTestServer(t, "", encodeLinks(
		glLink(7, "opened", []string{"status::todo"}, "is_blocked_by", "#7", "group/project#7"),
	))
	issue := fetchTodoCandidate(t, ts)

	require.Len(t, issue.BlockedBy, 1)
	b := issue.BlockedBy[0]
	require.NotNil(t, b.ID)
	assert.Equal(t, "7", *b.ID)
	require.NotNil(t, b.Identifier)
	assert.Equal(t, "#7", *b.Identifier)
	require.NotNil(t, b.State)
	assert.Equal(t, "todo", *b.State)
	require.NotNil(t, b.URL)
	assert.Equal(t, "https://gitlab.com/group/project/-/issues/7", *b.URL)
}

func TestNativeLinkOpenBlockerWithoutStatusLabelStaysUnresolved(t *testing.T) {
	ts := linksTestServer(t, "", encodeLinks(
		glLink(7, "opened", nil, "is_blocked_by", "#7", "group/project#7"),
	))
	issue := fetchTodoCandidate(t, ts)

	require.Len(t, issue.BlockedBy, 1)
	require.NotNil(t, issue.BlockedBy[0].State)
	assert.Equal(t, "opened", *issue.BlockedBy[0].State)
}

func TestNativeLinkClosedBlockerIsTerminal(t *testing.T) {
	ts := linksTestServer(t, "", encodeLinks(
		glLink(8, "closed", nil, "is_blocked_by", "#8", "group/project#8"),
	))
	issue := fetchTodoCandidate(t, ts)

	require.Len(t, issue.BlockedBy, 1)
	require.NotNil(t, issue.BlockedBy[0].State)
	assert.Equal(t, "done", *issue.BlockedBy[0].State)
}

func TestNativeLinkCrossProjectUsesFullReference(t *testing.T) {
	// No handler for /projects/pbf%2Fother/... — a re-fetch of the
	// cross-project blocker as a same-project iid would 404 and fail loudly
	// via the unexpected-path check below.
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "status::todo" {
			_ = json.NewEncoder(w).Encode([]any{glIssue(3, "Blocked issue", "opened", []string{"status::todo"})})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/3/links", encodeLinks(
		glLink(7, "opened", []string{"status::todo"}, "is_blocked_by", "pbf/other#7", "pbf/other#7"),
	))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL.EscapedPath())
		w.WriteHeader(http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	issue := fetchTodoCandidate(t, ts)
	require.Len(t, issue.BlockedBy, 1)
	b := issue.BlockedBy[0]
	require.NotNil(t, b.ID)
	assert.Equal(t, "pbf/other#7", *b.ID)
	require.NotNil(t, b.Identifier)
	assert.Equal(t, "pbf/other#7", *b.Identifier)
	require.NotNil(t, b.State)
	assert.Equal(t, "todo", *b.State)
}

func TestNativeLinkIgnoresNonBlockingLinkTypes(t *testing.T) {
	ts := linksTestServer(t, "", encodeLinks(
		glLink(7, "opened", nil, "relates_to", "#7", "group/project#7"),
		glLink(9, "opened", nil, "blocks", "#9", "group/project#9"),
	))
	issue := fetchTodoCandidate(t, ts)
	assert.Empty(t, issue.BlockedBy)
}

func TestNativeLinkDedupesWithDescriptionBlocker(t *testing.T) {
	ts := linksTestServer(t, "Blocked by #7", encodeLinks(
		glLink(7, "closed", []string{"status::done"}, "is_blocked_by", "#7", "group/project#7"),
	))
	// No /issues/7 handler: link payload must supply the state so the
	// description ref isn't re-fetched (a re-fetch would 404 → State nil).
	issue := fetchTodoCandidate(t, ts)

	require.Len(t, issue.BlockedBy, 1)
	require.NotNil(t, issue.BlockedBy[0].State)
	assert.Equal(t, "done", *issue.BlockedBy[0].State)
}

func TestNativeLinkFetchErrorKeepsIssueBlocked(t *testing.T) {
	ts := linksTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	issue := fetchTodoCandidate(t, ts)

	require.Len(t, issue.BlockedBy, 1, "links fetch failure must not silently drop dependencies")
	assert.Nil(t, issue.BlockedBy[0].State, "unknown dependency must stay unresolved")
	require.NotNil(t, issue.BlockedBy[0].Identifier)
	assert.Equal(t, linksUnavailableIdentifier, *issue.BlockedBy[0].Identifier)
}

func TestNativeLinkNotFoundMeansNoLinks(t *testing.T) {
	ts := linksTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	issue := fetchTodoCandidate(t, ts)
	assert.Empty(t, issue.BlockedBy)
}

func TestFetchIssuesByStatesSkipsLinksForTerminalIssues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{glIssue(5, "Done issue", "closed", []string{"status::done"})})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/5/links", func(w http.ResponseWriter, r *http.Request) {
		t.Error("links must not be fetched for terminal issues")
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	result, err := client.FetchIssuesByStates(context.Background(), []string{"done"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Empty(t, result[0].BlockedBy)
}

func TestFetchIssuesByStatesPopulatesNativeLinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "status::todo" {
			_ = json.NewEncoder(w).Encode([]any{glIssue(3, "Blocked issue", "opened", []string{"status::todo"})})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/3/links", encodeLinks(
		glLink(7, "opened", []string{"status::todo"}, "is_blocked_by", "#7", "group/project#7"),
	))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	result, err := client.FetchIssuesByStates(context.Background(), []string{"todo"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Len(t, result[0].BlockedBy, 1)
	assert.Equal(t, "#7", *result[0].BlockedBy[0].Identifier)
}
