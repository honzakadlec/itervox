package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

func TestHandleAgentMarkMerged_MovesToCompletionAndRefreshes(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionMarkMerged}, "", time.Minute)
	require.NoError(t, err)

	var gotIdentifier string
	refresh := make(chan struct{}, 1)
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.RefreshChan = refresh
	cfg.Client = &server.FuncClient{
		MarkIssueMergedFn: func(_ context.Context, identifier string) (string, error) {
			gotIdentifier = identifier
			return "done", nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/mark-merged", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-1", gotIdentifier)
	assert.Contains(t, w.Body.String(), `"state":"done"`)
	select {
	case <-refresh:
	default:
		t.Fatal("expected mark-merged to queue refresh")
	}
}

func TestHandleAgentMarkMerged_RequiresGrant(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionMoveState}, "", time.Minute)
	require.NoError(t, err)

	called := false
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		MarkIssueMergedFn: func(context.Context, string) (string, error) {
			called = true
			return "done", nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/mark-merged", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)
	assert.Contains(t, w.Body.String(), "agent_action_denied")
}

func TestHandleAgentMarkMerged_SurfacesClientError(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionMarkMerged}, "", time.Minute)
	require.NoError(t, err)

	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		MarkIssueMergedFn: func(context.Context, string) (string, error) {
			return "", errors.New("mark_merged: tracker.completion_state is not configured")
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/mark-merged", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "completion_state is not configured")
}
