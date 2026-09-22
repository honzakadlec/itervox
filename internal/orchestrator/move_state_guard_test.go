package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// These guard against a real production incident (DBIMPROVE-598): a
// deploy-checker automation profile was granted move_state, started polling
// an external deploy API, told the user it would "notify when terminal",
// and ended its turn without ever calling move_state. Itervox recorded the
// run as TerminalSucceeded and released the claim, so the issue never
// reached the state that would have fired the downstream visual-tester
// automation. RunEntry.RequiresMoveState + Orchestrator.MoveStateCountFor
// close that gap by rewriting such an exit to TerminalFailed so it retries
// instead of silently dropping the issue mid-pipeline.
func moveStateGuardCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"04-With Developer"}
	cfg.Tracker.TerminalStates = []string{"05-Code Review"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.Command = "codex"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"deploy-checker": {AllowedActions: []string{config.AgentActionMoveState}},
	}
	return cfg
}

func moveStateGuardRunEntry(issue domain.Issue) *RunEntry {
	attempt := 0
	return &RunEntry{
		Issue:        issue,
		Kind:         "automation",
		ProfileName:  "deploy-checker",
		AutomationID: "deploy-check",
		Automation: &AutomationDispatch{
			AutomationID: "deploy-check",
			ProfileName:  "deploy-checker",
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerIssueEnteredState,
				TriggerState: "04-With Developer",
			},
		},
		RequiresMoveState: true,
		StartedAt:         time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC),
		RetryAttempt:      &attempt,
	}
}

func TestEventWorkerExited_MoveStateGrantedButNeverCalled_RetriedNotSucceeded(t *testing.T) {
	cfg := moveStateGuardCfg()
	state := NewState(cfg)
	issue := domain.Issue{ID: "id1", Identifier: "DBIMPROVE-598", Title: "T", State: "04-With Developer"}
	state.Running[issue.ID] = moveStateGuardRunEntry(issue)
	state.Claimed[issue.ID] = struct{}{}

	o := New(cfg, nil, &agenttest.FakeRunner{Stall: true}, nil)
	attempt := 0
	out := o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalSucceeded,
			RetryAttempt:   &attempt,
		},
	})

	require.NotContains(t, out.Running, issue.ID, "issue must not sit in Running after the guard rewrites the exit")
	require.Contains(t, out.RetryAttempts, issue.ID, "a run that never called the granted move_state must be retried, not silently released")
	require.Equal(t, 1, out.RetryAttempts[issue.ID].Attempt)
	require.NotNil(t, out.RetryAttempts[issue.ID].Automation, "retry must replay via startAutomationRun using the original automation dispatch")
	require.Equal(t, "deploy-check", out.RetryAttempts[issue.ID].Automation.AutomationID)
	require.Contains(t, out.Claimed, issue.ID, "claim must be retained for the pending retry")
}

func TestEventWorkerExited_MoveStateGrantedAndCalled_TreatedAsRealSuccess(t *testing.T) {
	cfg := moveStateGuardCfg()
	state := NewState(cfg)
	issue := domain.Issue{ID: "id1", Identifier: "DBIMPROVE-598", Title: "T", State: "06-R4 QA"}
	state.Running[issue.ID] = moveStateGuardRunEntry(issue)
	state.Claimed[issue.ID] = struct{}{}

	o := New(cfg, nil, &agenttest.FakeRunner{Stall: true}, nil)
	o.BumpMoveStateCount(issue.Identifier) // simulates the HTTP handler recording the agent's real move_state call

	attempt := 0
	out := o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalSucceeded,
			RetryAttempt:   &attempt,
		},
	})

	require.NotContains(t, out.Running, issue.ID)
	require.NotContains(t, out.RetryAttempts, issue.ID, "a run that actually called move_state must not be retried")
	require.NotContains(t, out.Claimed, issue.ID, "claim must be released on real success")
}

func TestEventWorkerExited_MoveStateGuard_OnlyAppliesToAutomationKind(t *testing.T) {
	cfg := moveStateGuardCfg()
	state := NewState(cfg)
	issue := domain.Issue{ID: "id1", Identifier: "DBIMPROVE-598", Title: "T", State: "04-With Developer"}
	entry := moveStateGuardRunEntry(issue)
	entry.Kind = "worker" // e.g. UseIssueLifecycle automations, which follow normal worker semantics
	state.Running[issue.ID] = entry
	state.Claimed[issue.ID] = struct{}{}

	o := New(cfg, nil, &agenttest.FakeRunner{Stall: true}, nil)
	attempt := 0
	out := o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalSucceeded,
			RetryAttempt:   &attempt,
		},
	})

	require.NotContains(t, out.RetryAttempts, issue.ID, "the guard must not second-guess ordinary worker-kind runs")
	require.NotContains(t, out.Claimed, issue.ID)
}
