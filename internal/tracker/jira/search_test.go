package jira_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jiraclient "github.com/vnovick/itervox/internal/tracker/jira"
)

func searchResponse(issues ...map[string]any) map[string]any {
	rawIssues := make([]any, len(issues))
	for i, is := range issues {
		rawIssues[i] = is
	}
	return map[string]any{
		"issues": rawIssues,
		"isLast": true,
	}
}

// decodeSearchJQLBody reads the JQL search request body (POST /search/jql).
func decodeSearchJQLBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	return body
}

func TestFetchCandidateIssuesUsesProjectAndActiveStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		body := decodeSearchJQLBody(t, r)
		jql, _ := body["jql"].(string)
		assert.Contains(t, jql, `project = "PROJ"`)
		assert.Contains(t, jql, `status in ("In Progress")`)
		writeJSON(w, 200, searchResponse(jiraIssueFixture("1", "PROJ-1", "A", "In Progress")))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "PROJ-1", issues[0].Identifier)
}

func TestFetchCandidateIssuesMultiProjectSlug(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		body := decodeSearchJQLBody(t, r)
		jql, _ := body["jql"].(string)
		assert.Contains(t, jql, `project in ("PROJ","OTHER")`)
		writeJSON(w, 200, searchResponse(jiraIssueFixture("1", "PROJ-1", "A", "In Progress")))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.ProjectSlug = "PROJ, OTHER"
	client := jiraclient.NewClient(cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
}

func TestFetchCandidateIssuesNoActiveStatesSkipsCall(t *testing.T) {
	cfg := defaultConfig("http://should-not-be-called.invalid")
	cfg.ActiveStates = nil
	client := jiraclient.NewClient(cfg)
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestFetchIssuesByStatesEmptyReturnsEmptyNoCall(t *testing.T) {
	client := jiraclient.NewClient(defaultConfig("http://should-not-be-called.invalid"))
	issues, err := client.FetchIssuesByStates(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestFetchIssuesByStatesBuildsJQL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		body := decodeSearchJQLBody(t, r)
		jql, _ := body["jql"].(string)
		assert.Contains(t, jql, `status in ("Done","Cancelled")`)
		writeJSON(w, 200, searchResponse(jiraIssueFixture("2", "PROJ-2", "B", "Done")))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchIssuesByStates(context.Background(), []string{"Done", "Cancelled"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
}

func TestFetchIssueStatesByIDsEmptyReturnsEmptyNoCall(t *testing.T) {
	client := jiraclient.NewClient(defaultConfig("http://should-not-be-called.invalid"))
	issues, err := client.FetchIssueStatesByIDs(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestFetchIssueStatesByIDsBuildsJQL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		body := decodeSearchJQLBody(t, r)
		jql, _ := body["jql"].(string)
		assert.Contains(t, jql, `id in ("10001","10002")`)
		writeJSON(w, 200, searchResponse(
			jiraIssueFixture("10001", "PROJ-1", "A", "Done"),
			jiraIssueFixture("10002", "PROJ-2", "B", "In Progress"),
		))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchIssueStatesByIDs(context.Background(), []string{"10001", "10002"})
	require.NoError(t, err)
	require.Len(t, issues, 2)
}

func TestSearchPaginatesUntilTotalReached(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		body := decodeSearchJQLBody(t, r)
		token, _ := body["nextPageToken"].(string)
		calls++
		if token == "" {
			writeJSON(w, 200, map[string]any{
				"issues":        []any{jiraIssueFixture("1", "PROJ-1", "A", "In Progress")},
				"isLast":        false,
				"nextPageToken": "page-2",
			})
			return
		}
		assert.Equal(t, "page-2", token)
		writeJSON(w, 200, map[string]any{
			"issues": []any{jiraIssueFixture("2", "PROJ-2", "B", "In Progress")},
			"isLast": true,
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, 2, calls)
}
