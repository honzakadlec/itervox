package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func reviewRecheckHarness(t *testing.T, issues ...domain.Issue) (*Orchestrator, State, *gatedRunner) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.AutoReview = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"default":  {},
		"reviewer": {},
		"merger":   {AutoReview: new(bool)},
	}
	cfg.Tracker.ReviewState = "In Review"
	cfg.Tracker.CompletionState = "Done"
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, nil)
	o.SetPendingReviewsFile(filepath.Join(t.TempDir(), "pending_reviews.json"))
	t.Cleanup(func() { close(rr.release); o.workersWg.Wait(5 * time.Second) })
	return o, NewState(cfg), rr
}

// TestParkedReviewRequeuedWhenSiblingLands: a reviewer that could not merge
// (merge-order blocker open, CI red on a stale base) leaves its issue in
// review_state, which polling never dispatches. When a sibling moves
// review_state → completion_state, every other parked issue is re-queued and
// its reviewer runs — except issues whose profile opted out of auto-review.
func TestParkedReviewRequeuedWhenSiblingLands(t *testing.T) {
	parked := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	optedOut := domain.Issue{ID: "id-2", Identifier: "ENG-2", Title: "T", State: "In Review", Labels: []string{"profile::merger"}}
	active := domain.Issue{ID: "id-3", Identifier: "ENG-3", Title: "T", State: "Todo"}
	o, state, rr := reviewRecheckHarness(t, parked, optedOut, active)
	ctx := context.Background()

	state = o.handleEvent(ctx, state, OrchestratorEvent{
		Type:         EventIssueStatusChanged,
		StatusChange: &IssueStatusChange{Identifier: "ENG-9", FromState: "in review", ToState: "Done", At: time.Now()},
	})
	require.True(t, state.ReviewRecheckPending, "a review_state → completion_state move flags a recheck")

	now := time.Now()
	o.requeueParkedReviews(ctx, &state, now)
	assert.False(t, state.ReviewRecheckPending)
	assert.Contains(t, state.PendingReviews, "ENG-1")
	assert.Equal(t, "reviewer", state.PendingReviews["ENG-1"].Profile)
	assert.NotContains(t, state.PendingReviews, "ENG-2", "auto_review: false profile is not re-reviewed")
	assert.NotContains(t, state.PendingReviews, "ENG-3", "only review_state issues are re-queued")

	o.resumePendingReviews(ctx, &state, now)
	assert.True(t, reviewerRunning(state, "ENG-1"))
	assert.NotContains(t, state.PendingReviews, "ENG-1", "marker cleared once the reviewer starts")
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond)
}

// TestParkedReviewRecheckIgnoresOtherTransitions: only a landing (review_state
// → completion_state) flags a recheck; ordinary moves and a disabled
// auto_review do not re-queue anything.
func TestParkedReviewRecheckIgnoresOtherTransitions(t *testing.T) {
	parked := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	o, state, _ := reviewRecheckHarness(t, parked)
	ctx := context.Background()

	for _, ch := range []IssueStatusChange{
		{Identifier: "ENG-9", FromState: "In Progress", ToState: "In Review"},
		{Identifier: "ENG-9", FromState: "Todo", ToState: "Done"},
		{Identifier: "ENG-9", FromState: "In Review", ToState: "Cancelled"},
	} {
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventIssueStatusChanged, StatusChange: &ch})
		assert.False(t, state.ReviewRecheckPending, "%s → %s", ch.FromState, ch.ToState)
	}

	require.NoError(t, o.SetReviewerCfg("reviewer", false))
	state.ReviewRecheckPending = true
	o.requeueParkedReviews(ctx, &state, time.Now())
	assert.False(t, state.ReviewRecheckPending)
	assert.Empty(t, state.PendingReviews, "auto_review off: nothing is re-queued")
}
