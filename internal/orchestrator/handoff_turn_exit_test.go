package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// handoffTurnRunner optionally writes the run's handoff deliverable on its
// first turn and returns a distinct ResultText on every turn, so the worker's
// duplicate-result guard cannot end the turn loop on its own.
type handoffTurnRunner struct {
	mu           sync.Mutex
	calls        int
	wsPath       string
	writeHandoff bool
}

func (r *handoffTurnRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	calls := r.calls
	r.mu.Unlock()

	if r.writeHandoff && calls == 1 {
		const marker = "run.handoff_path: `"
		if _, rest, found := strings.Cut(prompt, marker); found {
			if end := strings.Index(rest, "`"); end > 0 {
				abs := filepath.Join(r.wsPath, rest[:end])
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
					_ = os.WriteFile(abs, []byte("## Done\n\nAll acceptance criteria met."), 0o644)
				}
			}
		}
	}

	return agent.TurnResult{
		SessionID:    "handoff-turn-session",
		InputTokens:  10,
		OutputTokens: 5,
		ResultText:   fmt.Sprintf("turn %d result", calls),
	}, nil
}

func (r *handoffTurnRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func runHandoffTurnScenario(t *testing.T, writeHandoff bool) (*handoffTurnRunner, *tracker.MemoryTracker) {
	t.Helper()
	wsDir := t.TempDir()

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 30
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }}."

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := &handoffTurnRunner{wsPath: wsDir, writeHandoff: writeHandoff}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return len(issues) == 1 && issues[0].State == "Done"
	}, 5*time.Second, 20*time.Millisecond, "worker should succeed and apply completion_state")
	return runner, mt
}

// A successful turn that wrote run.handoff_path has delivered the run's
// final deliverable. The worker must exit instead of re-prompting the agent
// with continuation turns while the issue is still in an active state —
// those turns made a finished agent think it was re-dispatched and escalate
// to input_required, so the success exit (and auto-review) never happened.
func TestWorkerExitsAfterTurnThatWritesHandoff(t *testing.T) {
	runner, _ := runHandoffTurnScenario(t, true)
	assert.Equal(t, 1, runner.callCount(),
		"worker should stop after the turn that wrote its handoff, not run continuation turns")
}

// Without a handoff the agent has not declared its deliverable, so the
// existing continuation behaviour (up to max_turns while active) is kept.
func TestWorkerContinuesWhenTurnWritesNoHandoff(t *testing.T) {
	runner, _ := runHandoffTurnScenario(t, false)
	assert.Equal(t, 3, runner.callCount(),
		"worker should keep running continuation turns up to max_turns when no handoff was written")
}
