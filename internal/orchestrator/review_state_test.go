package orchestrator_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

func reviewStateIssueState(t *testing.T, mt *tracker.MemoryTracker, id string) string {
	t.Helper()
	issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{id})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	return issues[0].State
}

// TestReviewState_ImplementerMovesToReviewNotCompletion: with review_state set,
// a successful implementer run lands in review_state, and the issue is not
// re-dispatched from there.
func TestReviewState_ImplementerMovesToReviewNotCompletion(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.ReviewState = "Review"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	done := make(chan struct{}, 8)
	runner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		return reviewStateIssueState(t, mt, "id1") == "Review"
	}, 4*time.Second, 20*time.Millisecond, "implementer success should move the issue to review_state")

	// Several poll intervals later: still in review, run exactly once.
	time.Sleep(300 * time.Millisecond)
	cancel()
	assert.Equal(t, "Review", reviewStateIssueState(t, mt, "id1"))
	assert.Len(t, done, 1, "issue in review_state must not be re-dispatched")
}

// TestReviewState_ReviewerRunAppliesNoTransition: the auto-review run that
// follows the implementer must not move the issue to completion_state on its
// own — only mark_merged does that.
func TestReviewState_ReviewerRunAppliesNoTransition(t *testing.T) {
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.ReviewState = "Review"
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.AutoReview = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "Review this code."},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	done := make(chan struct{}, 4)
	runner := &countingTrackingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: done,
	}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	for i := range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected implementer + reviewer RunTurn calls, got %d", i)
		}
	}
	// Wait for the reviewer worker to finish its post-run logic.
	require.Eventually(t, func() bool {
		return strings.Count(logBuf.String(), "worker: completed") >= 2
	}, 4*time.Second, 20*time.Millisecond, "reviewer run should complete")
	cancel()

	logs := logBuf.String()
	assert.Contains(t, logs, "orchestrator: dispatching reviewer")
	assert.Equal(t, "Review", reviewStateIssueState(t, mt, "id1"),
		"reviewer exit must leave the issue in review_state")
	assert.NotContains(t, logs, `target_state=Done`)
}

// TestReviewState_BlockerInReviewStaysUnresolved: a dependent issue is held
// while its blocker sits in review_state (non-terminal).
func TestReviewState_BlockerInReviewStaysUnresolved(t *testing.T) {
	cfg := baseConfig()
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.ReviewState = "Review"
	state := orchestrator.NewState(cfg)

	blockerID, blockerIdent, blockerState := "id4", "ENG-4", "Review"
	issue := makeIssue("id5", "ENG-5", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{ID: &blockerID, Identifier: &blockerIdent, State: &blockerState}}
	assert.False(t, orchestrator.IsEligible(issue, state, cfg), "blocker in review_state must hold the dependent")

	done := "Done"
	issue.BlockedBy[0].State = &done
	assert.True(t, orchestrator.IsEligible(issue, state, cfg), "blocker in completion_state releases the dependent")
}
