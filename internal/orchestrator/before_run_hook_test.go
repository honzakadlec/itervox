package orchestrator_test

import (
	"context"
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

// hookGateRunner records every agent turn and succeeds on the first one.
type hookGateRunner struct {
	mu         sync.Mutex
	prompts    []string
	sessionIDs []string
}

func (r *hookGateRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sid := ""
	if sessionID != nil {
		sid = *sessionID
	}
	r.sessionIDs = append(r.sessionIDs, sid)
	r.prompts = append(r.prompts, prompt)
	if sid != "" {
		return agent.TurnResult{SessionID: "hook-gate-session"}, nil // session concluded
	}
	return agent.TurnResult{SessionID: "hook-gate-session", InputTokens: 5, OutputTokens: 2, TotalTokens: 7, ResultText: "done"}, nil
}

func (r *hookGateRunner) snapshot() (prompts, sessionIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.prompts...), append([]string{}, r.sessionIDs...)
}

func readCount(t *testing.T, wsPath string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
	return string(b)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, failMsg func() string) {
	t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(failMsg())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A before_run exit 3 queues the hook's question as a sessionless
// input-required entry without starting the agent; the reply triggers a fresh
// dispatch that re-runs the hook, which then passes.
func TestBeforeRunHookExit3AsksWithoutAgentRunAndReplyRerunsHook(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }}"
	cfg.Hooks.BeforeRun = `printf x >> .before-run-count
if [ ! -f .answered ]; then
  echo "ENG-1 has no Fix Version. Set one in Jira and reply." > "$ITERVOX_HOOK_QUESTION_FILE"
  exit 3
fi`

	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	ct := newCommentTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	wsPath := filepath.Join(t.TempDir(), "workspace")
	runner := &hookGateRunner{}
	orch := orchestrator.New(cfg, ct, runner, &stableWorkspaceProvider{path: wsPath})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	waitFor(t, 3*time.Second, func() bool {
		_, ok := orch.Snapshot().InputRequiredIssues["ENG-1"]
		return ok && len(ct.commentsFor("id1")) > 0
	}, func() string { return "hook question was not queued as input-required" })

	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	assert.Equal(t, "ENG-1 has no Fix Version. Set one in Jira and reply.", entry.Context)
	assert.Empty(t, entry.SessionID, "a hook question has no agent session to resume")
	assert.Contains(t, ct.commentsFor("id1")[0].Body, "ENG-1 has no Fix Version")
	prompts, _ := runner.snapshot()
	assert.Empty(t, prompts, "the agent must not run when before_run asks")
	assert.Equal(t, "x", readCount(t, wsPath))

	// The human fixes the precondition, then replies.
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, ".answered"), nil, 0o644))
	require.NoError(t, orch.ProvideInput("ENG-1", "Fix Version set to 5.1.0 A."))

	waitFor(t, 3*time.Second, func() bool {
		issues, err := ct.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		return len(issues) > 0 && issues[0].State == "Done"
	}, func() string {
		prompts, _ := runner.snapshot()
		return "reply did not lead to a completed fresh run; prompts=" + strings.Join(prompts, " | ")
	})

	prompts, sessionIDs := runner.snapshot()
	require.NotEmpty(t, prompts)
	assert.Equal(t, "", sessionIDs[0], "the reply starts a fresh session")
	assert.Contains(t, prompts[0], "Work on ENG-1", "a fresh dispatch renders the workflow prompt")
	assert.Contains(t, prompts[0], "Fix Version set to 5.1.0 A.", "the reply is carried into the fresh prompt")
	assert.Equal(t, "xx", readCount(t, wsPath), "the reply must re-run before_run")
	_, stillWaiting := orch.Snapshot().InputRequiredIssues["ENG-1"]
	assert.False(t, stillWaiting)
}

// A before_run exit 4 defers: no agent run, no retry consumed, re-checked at
// max_retry_backoff_ms with the hook's reason on the retry row.
func TestBeforeRunHookExit4DefersWithoutAgentRunOrRetry(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }}"
	cfg.Hooks.BeforeRun = `printf x >> .before-run-count
if [ ! -f .ready ]; then
  echo "checked https://gitlab.example/mr/7"
  echo "gate MR !7 not merged"
  exit 4
fi`

	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	wsPath := filepath.Join(t.TempDir(), "workspace")
	runner := &hookGateRunner{}
	orch := orchestrator.New(cfg, mt, runner, &stableWorkspaceProvider{path: wsPath})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	// Three deferrals exceed max_retries=1 if any of them consumed a retry.
	waitFor(t, 3*time.Second, func() bool {
		return len(readCount(t, wsPath)) >= 3 && orch.Snapshot().RetryAttempts["id1"] != nil
	}, func() string { return "hook was not re-checked after deferring; count=" + readCount(t, wsPath) })

	retry := orch.Snapshot().RetryAttempts["id1"]
	require.NotNil(t, retry)
	require.NotNil(t, retry.Error)
	assert.Equal(t, "deferred: gate MR !7 not merged", *retry.Error)
	assert.Equal(t, 0, retry.Attempt, "a deferral must not consume a retry")
	prompts, _ := runner.snapshot()
	assert.Empty(t, prompts, "the agent must not run while deferred")
	_, paused := orch.Snapshot().PausedIdentifiers["ENG-1"]
	assert.False(t, paused, "deferrals must not exhaust retries")

	require.NoError(t, os.WriteFile(filepath.Join(wsPath, ".ready"), nil, 0o644))
	waitFor(t, 3*time.Second, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		return len(issues) > 0 && issues[0].State == "Done"
	}, func() string { return "issue did not complete once the hook passed" })
	prompts, _ = runner.snapshot()
	assert.NotEmpty(t, prompts)
}
