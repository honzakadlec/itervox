package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// stateRecordingTracker records every UpdateIssueState target and can fail
// FetchIssueStatesByIDs from the Nth call on (0 = never).
type stateRecordingTracker struct {
	*tracker.MemoryTracker
	mu          sync.Mutex
	moves       []string
	fetches     atomic.Int32
	failFetchAt int32
}

func (s *stateRecordingTracker) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	s.mu.Lock()
	s.moves = append(s.moves, stateName)
	s.mu.Unlock()
	return s.MemoryTracker.UpdateIssueState(ctx, issueID, stateName)
}

func (s *stateRecordingTracker) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	if n := s.fetches.Add(1); s.failFetchAt > 0 && n >= s.failFetchAt {
		return nil, errors.New("tracker unavailable")
	}
	return s.MemoryTracker.FetchIssueStatesByIDs(ctx, ids)
}

func (s *stateRecordingTracker) recordedMoves() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.moves...)
}

// movingRunner completes one turn; when moveTo is set it first moves the
// issue itself, like an agent calling `itervox action move_state`.
type movingRunner struct {
	tr     *tracker.MemoryTracker
	moveTo string
	done   chan struct{}
	once   sync.Once
}

func (r *movingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	if r.moveTo != "" {
		_ = r.tr.UpdateIssueState(ctx, "id1", r.moveTo)
	}
	r.once.Do(func() { close(r.done) })
	return agent.TurnResult{SessionID: "s1", ResultText: "done", TotalTokens: 1}, nil
}

// runCompletionScenario runs one single-turn worker on ENG-1 with
// completion_state "In Review" and returns the recorded state moves and the
// issue's final state.
func runCompletionScenario(t *testing.T, agentMoveTo string, failFetchAt int32) ([]string, string) {
	t.Helper()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "In Review"

	mem := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	tr := &stateRecordingTracker{MemoryTracker: mem, failFetchAt: failFetchAt}
	runner := &movingRunner{tr: mem, moveTo: agentMoveTo, done: make(chan struct{})}
	orch := orchestrator.New(cfg, tr, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-runner.done:
	case <-ctx.Done():
		t.Fatal("worker did not run")
	}
	// Let the worker finish its post-run transition (and its retries).
	time.Sleep(500 * time.Millisecond)

	issues, err := mem.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	return tr.recordedMoves(), issues[0].State
}

func TestCompletionTransitionSkippedWhenAgentMovedIssue(t *testing.T) {
	moves, final := runCompletionScenario(t, "Done", 0)
	assert.Equal(t, "Done", final, "the agent's explicit move must survive the run's end")
	assert.NotContains(t, moves, "In Review", "itervox must not apply completion_state over an agent move")
}

func TestCompletionTransitionRunsWhenIssueStillActive(t *testing.T) {
	moves, final := runCompletionScenario(t, "", 0)
	assert.Equal(t, "In Review", final)
	assert.Contains(t, moves, "In Review")
}

func TestCompletionTransitionFallsBackOnFetchError(t *testing.T) {
	// Fetch #1 is the turn loop's pre-turn refresh; from #2 on (the
	// completion check) the tracker fails, so itervox must transition as before.
	moves, final := runCompletionScenario(t, "", 2)
	assert.Equal(t, "In Review", final)
	assert.Contains(t, moves, "In Review")
}
