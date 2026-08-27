package jira_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jiraclient "github.com/vnovick/itervox/internal/tracker/jira"
)

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func TestCreateCommentPostsADFBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		reqBody := decodeBody(t, r)
		body, _ := reqBody["body"].(map[string]any)
		assert.Equal(t, "doc", body["type"])
		writeJSON(w, 201, map[string]any{
			"id":      "555",
			"author":  map[string]any{"displayName": "Bot", "accountId": "abc"},
			"created": "2024-06-01T10:15:00.000+0000",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	comment, err := client.CreateComment(context.Background(), "PROJ-1", "hello world")
	require.NoError(t, err)
	require.NotNil(t, comment)
	assert.Equal(t, "555", comment.ID)
	assert.Equal(t, "hello world", comment.Body)
	assert.Equal(t, "Bot", comment.AuthorName)
}

func TestSetIssueBranchPostsMarkerComment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		reqBody := decodeBody(t, r)
		body, _ := reqBody["body"].(map[string]any)
		content, _ := body["content"].([]any)
		para, _ := content[0].(map[string]any)
		paraContent, _ := para["content"].([]any)
		textNode, _ := paraContent[0].(map[string]any)
		assert.Equal(t, "itervox:branch:feature-x", textNode["text"])
		writeJSON(w, 201, map[string]any{"id": "1"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	err := client.SetIssueBranch(context.Background(), "PROJ-1", "feature-x")
	require.NoError(t, err)
}

func TestUpdateIssueStateFindsAndPostsTransition(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{
				"transitions": []any{
					map[string]any{"id": "11", "name": "Start", "to": map[string]any{"name": "In Progress"}},
					map[string]any{"id": "21", "name": "Done", "to": map[string]any{"name": "Done"}},
				},
			})
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		reqBody := decodeBody(t, r)
		transition, _ := reqBody["transition"].(map[string]any)
		assert.Equal(t, "21", transition["id"])
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	err := client.UpdateIssueState(context.Background(), "PROJ-1", "Done")
	require.NoError(t, err)
}

func TestUpdateIssueStateNoMatchingTransitionErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue/PROJ-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"transitions": []any{
				map[string]any{"id": "11", "name": "Start", "to": map[string]any{"name": "In Progress"}},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	err := client.UpdateIssueState(context.Background(), "PROJ-1", "Nonexistent")
	require.Error(t, err)
}

func TestCreateIssuePostsFieldsAndFetchesDetail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		reqBody := decodeBody(t, r)
		fields, _ := reqBody["fields"].(map[string]any)
		project, _ := fields["project"].(map[string]any)
		assert.Equal(t, "PROJ", project["key"])
		assert.Equal(t, "New title", fields["summary"])
		issuetype, _ := fields["issuetype"].(map[string]any)
		assert.Equal(t, "Task", issuetype["name"])
		writeJSON(w, 201, map[string]any{"id": "20001", "key": "PROJ-20"})
	})
	mux.HandleFunc("/rest/api/3/issue/PROJ-20", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jiraIssueFixture("20001", "PROJ-20", "New title", "To Do"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.CreateIssue(context.Background(), "PROJ-1", "New title", "body text", "")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "PROJ-20", issue.Identifier)
}

func TestCreateIssueWithStateNameTransitions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 201, map[string]any{"id": "20002", "key": "PROJ-21"})
	})
	transitioned := false
	mux.HandleFunc("/rest/api/3/issue/PROJ-21/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{
				"transitions": []any{
					map[string]any{"id": "31", "name": "Start", "to": map[string]any{"name": "In Progress"}},
				},
			})
			return
		}
		transitioned = true
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/rest/api/3/issue/PROJ-21", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jiraIssueFixture("20002", "PROJ-21", "New title", "In Progress"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := jiraclient.NewClient(defaultConfig(ts.URL))
	_, err := client.CreateIssue(context.Background(), "PROJ-1", "New title", "body", "In Progress")
	require.NoError(t, err)
	assert.True(t, transitioned)
}
