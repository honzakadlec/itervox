package orchestrator_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// runAutoReviewScenario runs one successful worker for ENG-1 (routed by the
// given labels) with global auto_review on, and reports whether the reviewer
// was dispatched.
func runAutoReviewScenario(t *testing.T, labels []string, profiles map[string]config.AgentProfile) bool {
	t.Helper()
	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.AutoReview = true
	cfg.Agent.Profiles = profiles

	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.Labels = labels
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	done := make(chan struct{}, 2)
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

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not run")
	}
	// Give the event loop time to process the exit and (maybe) dispatch the reviewer.
	time.Sleep(400 * time.Millisecond)
	return strings.Contains(logBuf.String(), "orchestrator: dispatching reviewer")
}

func TestAutoReviewSkippedForProfileWithAutoReviewFalse(t *testing.T) {
	off := false
	reviewed := runAutoReviewScenario(t, []string{"profile::tester"}, map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "Review this code."},
		"tester":   {Command: "claude", Prompt: "Run tests.", AutoReview: &off},
	})
	assert.False(t, reviewed, "a profile with auto_review: false must not queue the reviewer")
}

func TestAutoReviewStillRunsForDefaultProfile(t *testing.T) {
	reviewed := runAutoReviewScenario(t, nil, map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Prompt: "Review this code."},
	})
	assert.True(t, reviewed, "a run without a profile opting out must still queue the reviewer")
}
