package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestDispatchUsesProfileFromLabel verifies a profile::<name> tracker label
// routes a fresh dispatch to that profile instead of agent.default_profile.
func TestDispatchUsesProfileFromLabel(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Command = "claude --model bare-default"
	cfg.Agent.DefaultProfile = "implementer"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude --model implementer-model", Backend: "claude"},
		"tester":      {Command: "claude --model tester-model", Backend: "claude"},
	}

	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.Labels = []string{"profile::tester"}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &commandRecordingRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()
	defer func() { cancel(); <-runDone }()

	deadline := time.After(3 * time.Second)
	for {
		if cmds := runner.snapshot(); len(cmds) > 0 {
			assert.Contains(t, cmds[0], "tester-model",
				"profile::tester label must dispatch with the tester profile")
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker never ran; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestWorkerHooksReceiveProfile verifies after_create, before_run and after_run
// see the run's resolved profile as ITERVOX_PROFILE, so project hooks can skip
// setup that only some profiles need.
func TestWorkerHooksReceiveProfile(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"tester": {}}
	outDir := t.TempDir()
	capture := func(name string) string {
		return `printf '%s\n' "$ITERVOX_PROFILE" >> ` + filepath.Join(outDir, name)
	}
	cfg.Hooks.AfterCreate = capture("after_create")
	cfg.Hooks.BeforeRun = capture("before_run")
	cfg.Hooks.AfterRun = capture("after_run")

	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.Labels = []string{"profile::tester"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	wm := &stableWorkspaceProvider{path: filepath.Join(t.TempDir(), "workspace")}
	orch := orchestrator.New(cfg, mt, &succeedOnceRunner{}, wm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(outDir, name))
		return strings.TrimSpace(string(b))
	}
	deadline := time.After(4 * time.Second)
	for read("after_run") == "" {
		select {
		case <-deadline:
			t.Fatalf("after_run hook did not run; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
	assert.Equal(t, "tester", read("after_create"))
	assert.Equal(t, "tester", read("before_run"))
	assert.Equal(t, "tester", read("after_run"))
}
