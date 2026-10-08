package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchIssueDetailPopulatesCommentsAndBranch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues/3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(3, "Issue 3", "opened", []string{"status::in-progress"}))
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/3/notes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"id": float64(1), "body": "regular comment", "created_at": "2024-01-01T00:00:00Z",
				"author": map[string]any{"id": float64(7), "username": "alice"}, "system": false},
			map[string]any{"id": float64(2), "body": itervoxBranchPrefix + "feature/foo -->", "created_at": "2024-01-02T00:00:00Z",
				"author": map[string]any{"id": float64(7), "username": "alice"}, "system": false},
			map[string]any{"id": float64(3), "body": "changed label to in-progress", "created_at": "2024-01-03T00:00:00Z",
				"author": map[string]any{"id": float64(7), "username": "alice"}, "system": true},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issue, err := client.FetchIssueDetail(context.Background(), "3")
	require.NoError(t, err)
	require.Len(t, issue.Comments, 1, "system note and branch marker note must be excluded")
	assert.Equal(t, "regular comment", issue.Comments[0].Body)
	require.NotNil(t, issue.BranchName)
	assert.Equal(t, "feature/foo", *issue.BranchName)
}

func TestFetchIssueByIdentifierStripsHash(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/issues/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(42, "Issue 42", "opened", []string{"status::todo"}))
	})
	mux.HandleFunc("/projects/group%2Fproject/issues/42/notes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issue, err := client.FetchIssueByIdentifier(context.Background(), "#42")
	require.NoError(t, err)
	assert.Equal(t, "#42", issue.Identifier)
}

func TestCreateCommentPostsNote(t *testing.T) {
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/projects/group%2Fproject/issues/5/notes", r.URL.EscapedPath())
		assert.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": float64(99), "body": gotBody["body"], "created_at": "2024-01-01T00:00:00Z",
			"author": map[string]any{"id": float64(1), "username": "bot"},
		})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	comment, err := client.CreateComment(context.Background(), "5", "hello world")
	require.NoError(t, err)
	assert.Equal(t, "hello world", gotBody["body"])
	assert.Equal(t, "99", comment.ID)
	assert.Equal(t, "bot", comment.AuthorName)
}

func TestCreateIssueSetsLabelWhenStateGiven(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/projects/group%2Fproject/issues", r.URL.EscapedPath())
		assert.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glIssue(9, gotBody["title"].(string), "opened", []string{"status::todo"}))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	issue, err := client.CreateIssue(context.Background(), "1", "Follow-up", "body text", "todo")
	require.NoError(t, err)
	assert.Equal(t, "status::todo", gotBody["labels"])
	assert.Equal(t, "#9", issue.Identifier)
}

func TestUpdateIssueStateAddsCompletionCloseEvent(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	err := client.UpdateIssueState(context.Background(), "5", "done")
	require.NoError(t, err)
	assert.Equal(t, "status::done", gotBody["add_labels"])
	assert.Equal(t, "close", gotBody["state_event"])
}

func TestUpdateIssueStateReopensOnActiveState(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	err := client.UpdateIssueState(context.Background(), "5", "todo")
	require.NoError(t, err)
	assert.Equal(t, "status::todo", gotBody["add_labels"])
	assert.Equal(t, "reopen", gotBody["state_event"])
}

func TestUpdateIssueStateNoStateEventForOtherTerminalState(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	err := client.UpdateIssueState(context.Background(), "5", "cancelled")
	require.NoError(t, err)
	assert.Equal(t, "status::cancelled", gotBody["add_labels"])
	_, hasEvent := gotBody["state_event"]
	assert.False(t, hasEvent, "non-completion terminal states must not close or reopen the issue")
}

func TestSetIssueBranchPostsMarkerComment(t *testing.T) {
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "created_at": "2024-01-01T00:00:00Z"})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	err := client.SetIssueBranch(context.Background(), "5", "feature/foo")
	require.NoError(t, err)
	assert.Equal(t, itervoxBranchPrefix+"feature/foo -->", gotBody["body"])
}
