package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

type blockedRunner struct{}

func (r *blockedRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func testConfig() *config.Config {
	return &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"In Progress"},
			TerminalStates: []string{"Done"},
		},
		Polling: config.PollingConfig{IntervalMs: 20},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 1,
			MaxTurns:            3,
			ReadTimeoutMs:       1000,
			TurnTimeoutMs:       1000,
		},
		PromptTemplate: "Handle {{ issue.identifier }}",
	}
}

func TestProvideInputSeedsAgentSessionIDNotRunLogSessionID(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID:    "id1",
		Identifier: "ENG-1",
		SessionID:  "agent-session-1",
		Context:    "Need approval",
		Backend:    "codex",
		Command:    "codex",
		QueuedAt:   time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	state = orch.handleEvent(ctx, state, OrchestratorEvent{
		Type:       EventProvideInput,
		Identifier: "ENG-1",
		Message:    "Approved.",
	})
	cancel()

	entry, ok := state.Running["id1"]
	require.True(t, ok)
	require.NotNil(t, entry)
	assert.Empty(t, entry.SessionID, "run log session ID should stay empty until the worker publishes its own run log ID")
	assert.Equal(t, "agent-session-1", entry.AgentSessionID, "the recovered agent session belongs in AgentSessionID, not SessionID")
}

func TestPendingInputResumeSurvivesRetryableWorkerFailure(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		SessionID:   "agent-session-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		Backend:     "codex",
		Command:     "codex",
		QueuedAt:    time.Now(),
	}
	state.Running["id1"] = &RunEntry{
		Issue:              issue,
		AgentSessionID:     "agent-session-1",
		PendingInputResume: true,
		StartedAt:          time.Now(),
	}
	state.Claimed["id1"] = struct{}{}

	state = orch.handleEvent(context.Background(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalFailed,
		},
		Error: assert.AnError,
	})

	entry, ok := state.PendingInputResumes["ENG-1"]
	require.True(t, ok, "retryable failure before progress must preserve the pending reply")
	require.NotNil(t, entry)
	assert.Equal(t, "Approved.", entry.UserMessage)
}

func TestPendingInputResumeClearsOnSuccess(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "Done",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		SessionID:   "agent-session-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		Backend:     "codex",
		Command:     "codex",
		QueuedAt:    time.Now(),
	}
	state.Running["id1"] = &RunEntry{
		Issue:              issue,
		AgentSessionID:     "agent-session-1",
		PendingInputResume: true,
		StartedAt:          time.Now(),
	}
	state.Claimed["id1"] = struct{}{}

	state = orch.handleEvent(context.Background(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalSucceeded,
		},
	})

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "successful resumed run should clear the pending reply")
}

func TestPendingInputResumeClearsWhenIssueIsPaused(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}
	state.PausedIdentifiers["ENG-1"] = "id1"

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "paused issues should drop stale pending replies")
}

func TestPendingInputResumeClearsWhenIssueLeavesActiveState(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "Backlog",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "non-active issues should drop stale pending replies")
}

// TestPendingInputResumeSurvivesCompletionState covers an automation-dispatched
// worker (e.g. a visual-tester profile fired by issue_entered_state on the
// configured completion_state) that asks for input while sitting outside
// ActiveStates. Regression test for the bug where any such reply was dropped
// as "stale" because only ActiveStates was checked.
func TestPendingInputResumeSurvivesCompletionState(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.CompletionState = "With Developer"
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "With Developer",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, running := state.Running["id1"]
	assert.True(t, running, "issue sitting in completion_state should still resume, not be dropped as stale")
}

// TestPendingInputResumeSurvivesAutomationTargetState covers the same bug for
// an issue_entered_state automation's trigger.state (e.g. a QA-stage
// visual-tester dispatch), which is neither ActiveStates nor CompletionState.
func TestPendingInputResumeSurvivesAutomationTargetState(t *testing.T) {
	cfg := testConfig()
	cfg.Automations = []config.AutomationConfig{
		{
			ID:      "visual-check",
			Enabled: true,
			Profile: "visual-tester",
			Trigger: config.AutomationTriggerConfig{
				Type:  config.AutomationTriggerIssueEnteredState,
				State: "QA",
			},
		},
	}
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "QA",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, running := state.Running["id1"]
	assert.True(t, running, "issue sitting in an automation trigger.state should still resume, not be dropped as stale")
}

// TestResumedAutomationRunSurvivesReconcileAfterInputRequired is the
// regression test for the bug found live on DBIMPROVE-584: a visual-tester
// run hit TerminalInputRequired while the issue sat in a non-active
// automation trigger state (06-R4 QA-equivalent here: "QA"). The user
// replied (ProvideInput), the run resumed via processPendingInputResumes,
// and the very next ReconcileTrackerStates pass killed it mid-turn because
// the resumed RunEntry wasn't tagged Kind=="automation" — reconcile's
// ActiveStates gate doesn't apply to automation-kind runs (see
// reconcile.go's exemption), but only if Kind survived the
// InputRequiredEntry -> PendingInputResumeEntry -> RunEntry round trip.
func TestResumedAutomationRunSurvivesReconcileAfterInputRequired(t *testing.T) {
	cfg := testConfig()
	cfg.Automations = []config.AutomationConfig{
		{
			ID:      "visual-check",
			Enabled: true,
			Profile: "visual-tester",
			Trigger: config.AutomationTriggerConfig{
				Type:  config.AutomationTriggerIssueEnteredState,
				State: "QA",
			},
		},
	}
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "QA",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	// Mirrors what queueInputRequiredEntry now populates on InputRequiredEntry
	// (and buildPendingInputResumeEntry carries into PendingInputResumeEntry)
	// for a run originally dispatched by an issue_entered_state automation.
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:      "id1",
		Identifier:   "ENG-1",
		Context:      "Need approval",
		UserMessage:  "Approved.",
		QueuedAt:     time.Now(),
		Kind:         "automation",
		AutomationID: "visual-check",
		TriggerType:  string(config.AutomationTriggerIssueEnteredState),
	}

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	running, ok := state.Running["id1"]
	require.True(t, ok, "resumed run should be dispatched")
	assert.Equal(t, "automation", running.Kind, "resumed RunEntry must carry Kind=\"automation\" through from the InputRequiredEntry")

	events := make(chan OrchestratorEvent, 10)
	state = ReconcileTrackerStates(context.Background(), state, mt, events, nil)

	_, stillRunning := state.Running["id1"]
	assert.True(t, stillRunning, "reconcile must not kill a resumed automation-kind run sitting in a non-active trigger state")
}
