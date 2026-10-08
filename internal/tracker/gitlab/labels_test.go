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

func TestEnsureStatusLabelsCreatesOnlyMissing(t *testing.T) {
	var created []string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/labels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]any{
				map[string]any{"name": "status::todo"},
				map[string]any{"name": "priority::high"},
			})
		case http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body["name"])
			assert.Equal(t, defaultLabelColor, body["color"])
			_ = json.NewEncoder(w).Encode(map[string]any{"name": body["name"]})
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := defaultTestConfig()
	cfg.ActiveStates = []string{"todo", "in-progress"}
	cfg.TerminalStates = []string{"done", "cancelled"}
	client := newTestClient(ts.URL, cfg)

	err := client.EnsureStatusLabels(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"status::in-progress", "status::done", "status::cancelled"}, created)
}

func TestEnsureStatusLabelsNoopWhenAllExist(t *testing.T) {
	posted := false
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/group%2Fproject/labels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posted = true
			return
		}
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"name": "status::todo"},
			map[string]any{"name": "status::in-progress"},
			map[string]any{"name": "status::done"},
			map[string]any{"name": "status::cancelled"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := newTestClient(ts.URL, defaultTestConfig())
	err := client.EnsureStatusLabels(context.Background())
	require.NoError(t, err)
	assert.False(t, posted, "must not POST when every wanted label already exists")
}

func TestEnsureStatusLabelsNoConfiguredStatesIsNoop(t *testing.T) {
	client := newTestClient("http://should-not-be-called.invalid", ClientConfig{ProjectSlug: "group/project"})
	err := client.EnsureStatusLabels(context.Background())
	require.NoError(t, err)
}
