package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// fetchFailingTracker fails every FetchIssueStatesByIDs call, as a tracker
// does while the network is down.
type fetchFailingTracker struct {
	tracker.Tracker
}

func (fetchFailingTracker) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, errors.New("dial tcp: lookup gitlab.com: no such host")
}

// A retry whose tracker re-fetch fails was never dispatched, so it must not
// consume an attempt. Before the fix every failed poll added one, so a
// night-long outage burned the budget (seen as "max retries exhausted
// (109/5)" on the first real failure afterwards).
func TestRetryTrackerFetchFailureDoesNotConsumeAttempt(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = 5
	cfg.Agent.MaxRetryBackoffMs = 300000

	o := newOrchestratorForTest(cfg)
	o.tracker = fetchFailingTracker{Tracker: tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}

	now := time.Now()
	state := NewState(cfg)
	state = ScheduleRetry(state, "id1", 2, "ENG-1", "worker failed", now.Add(-time.Minute), 0, nil)

	for i := range 10 {
		tick := now.Add(time.Duration(i) * time.Hour) // always past the previous DueAt
		state = o.fireRetries(context.Background(), state, tick)

		entry, ok := state.RetryAttempts["id1"]
		require.True(t, ok, "poll %d: retry must stay queued while the tracker is unreachable", i)
		assert.Equal(t, 2, entry.Attempt, "poll %d: failed tracker fetch must not consume an attempt", i)
		assert.True(t, entry.DueAt.After(tick), "poll %d: retry must be pushed back, not fired again immediately", i)
	}
	_, claimed := state.Claimed["id1"]
	assert.True(t, claimed, "issue stays claimed while its retry waits")
}
