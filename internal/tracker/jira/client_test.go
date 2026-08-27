package jira_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jiraclient "github.com/vnovick/itervox/internal/tracker/jira"
)

func defaultConfig(endpoint string) jiraclient.ClientConfig {
	return jiraclient.ClientConfig{
		APIKey:         "test-token",
		Username:       "bot@example.com",
		Endpoint:       endpoint,
		ProjectSlug:    "PROJ",
		ActiveStates:   []string{"In Progress"},
		TerminalStates: []string{"Done"},
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func jiraIssueFixture(id, key, summary, status string) map[string]any {
	return map[string]any{
		"id":  id,
		"key": key,
		"fields": map[string]any{
			"summary": summary,
			"status":  map[string]any{"name": status},
		},
	}
}

func TestFetchIssueDetailBasic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-1", func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Basic "))
		writeJSON(w, 200, jiraIssueFixture("10001", "PROJ-1", "Fix the bug", "In Progress"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.FetchIssueDetail(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "10001", issue.ID)
	assert.Equal(t, "PROJ-1", issue.Identifier)
	assert.Equal(t, "Fix the bug", issue.Title)
	assert.Equal(t, "In Progress", issue.State)
}

func TestFetchIssueDetailNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	_, err := client.FetchIssueDetail(context.Background(), "PROJ-404")
	require.Error(t, err)
}

func TestFetchIssueDetailBackfillsBlockerState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-2", func(w http.ResponseWriter, r *http.Request) {
		issue := jiraIssueFixture("10002", "PROJ-2", "Blocked issue", "To Do")
		issue["fields"].(map[string]any)["issuelinks"] = []any{
			map[string]any{
				"type":        map[string]any{"inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": map[string]any{"id": "10001", "key": "PROJ-1"},
			},
		}
		writeJSON(w, 200, issue)
	})
	mux.HandleFunc("/rest/api/3/issue/10001", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jiraIssueFixture("10001", "PROJ-1", "Blocker issue", "Done"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.FetchIssueDetail(context.Background(), "PROJ-2")
	require.NoError(t, err)
	require.Len(t, issue.BlockedBy, 1)
	require.NotNil(t, issue.BlockedBy[0].State)
	assert.Equal(t, "Done", *issue.BlockedBy[0].State)
}

func TestFetchIssueDetailBlockerFetchErrorLeavesStateNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-3", func(w http.ResponseWriter, r *http.Request) {
		issue := jiraIssueFixture("10003", "PROJ-3", "Blocked issue", "To Do")
		issue["fields"].(map[string]any)["issuelinks"] = []any{
			map[string]any{
				"type":        map[string]any{"inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": map[string]any{"id": "99999", "key": "PROJ-99"},
			},
		}
		writeJSON(w, 200, issue)
	})
	mux.HandleFunc("/rest/api/3/issue/99999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.FetchIssueDetail(context.Background(), "PROJ-3")
	require.NoError(t, err)
	require.Len(t, issue.BlockedBy, 1)
	assert.Nil(t, issue.BlockedBy[0].State)
}

func TestFetchIssueByIdentifierDelegatesToDetail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jiraIssueFixture("10007", "PROJ-7", "Add feature", "To Do"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.FetchIssueByIdentifier(context.Background(), "PROJ-7")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "PROJ-7", issue.Identifier)
}
