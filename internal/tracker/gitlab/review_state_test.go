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

func reviewTestConfig() ClientConfig {
	cfg := defaultTestConfig()
	cfg.ReviewState = "review"
	return cfg
}

func TestIssueStateMapsOpenReviewLabel(t *testing.T) {
	c := newTestClient("http://unused", reviewTestConfig())
	assert.Equal(t, "review", c.issueState(glIssue(4, "t", "opened", []string{"status::review"})))
	assert.Equal(t, "todo", c.issueState(glIssue(4, "t", "opened", []string{"status::todo"})))
	// Closed always wins: a closed issue is terminal whatever its label says.
	assert.Equal(t, "done", c.issueState(glIssue(4, "t", "closed", []string{"status::review"})))
}

func TestIssueStateIgnoresReviewLabelWhenUnconfigured(t *testing.T) {
	c := newTestClient("http://unused", defaultTestConfig())
	assert.Empty(t, c.issueState(glIssue(4, "t", "opened", []string{"status::review"})))
}

func TestUpdateIssueStateToReviewKeepsIssueOpen(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer ts.Close()

	client := newTestClient(ts.URL, reviewTestConfig())
	require.NoError(t, client.UpdateIssueState(context.Background(), "4", "review"))
	assert.Equal(t, "status::review", gotBody["add_labels"])
	_, hasEvent := gotBody["state_event"]
	assert.False(t, hasEvent, "review_state must not close or reopen the issue")
}

func TestEnsureStatusLabelsCreatesReviewLabel(t *testing.T) {
	var created []string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/labels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]any{
				map[string]any{"name": "status::todo"},
				map[string]any{"name": "status::in-progress"},
				map[string]any{"name": "status::done"},
				map[string]any{"name": "status::cancelled"},
			})
		case http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body["name"])
			_ = json.NewEncoder(w).Encode(map[string]any{"name": body["name"]})
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, reviewTestConfig())
	require.NoError(t, client.EnsureStatusLabels(context.Background()))
	assert.Equal(t, []string{"status::review"}, created)
}
