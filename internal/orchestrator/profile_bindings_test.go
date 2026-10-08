package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// Profile SOUL.md / INSTRUCTIONS.md are Liquid-rendered with the same bindings
// as the WORKFLOW.md body, so `{{ workspace.base_branch }}` resolves there too
// instead of rendering empty.
func TestProfilePromptBlocksSeeWorkspaceBinding(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 30
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Workspace.BaseBranch = "integration-base"
	cfg.PromptTemplate = "Implement {{ issue.identifier }}."
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {
			Command:      "claude",
			Soul:         "Soul base=[{{ workspace.base_branch }}]",
			Instructions: "Instructions base=[{{ workspace.base_branch }}] handoff=[{{ run.handoff_path }}]",
		},
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetIssueProfile("ENG-1", "implementer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	select {
	case <-runner.done:
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not run within 4s")
	}
	cancel()

	_, prompts := runner.snapshot()
	require.NotEmpty(t, prompts)
	p := prompts[0]
	assert.Contains(t, p, "Soul base=[integration-base]")
	assert.Contains(t, p, "Instructions base=[integration-base]")
	assert.NotContains(t, p, "handoff=[]", "run bindings must still reach profile blocks")
}
