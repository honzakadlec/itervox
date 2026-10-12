package orchestrator

// An input-required run's Command is persisted and reused verbatim when the
// human replies. It must be the bare agent command: the run-scoped action
// bridge env (ITERVOX_ACTION_TOKEN & co.) is revoked when the run exits, and
// a stale prefix left in the stored command shadows the resumed run's fresh
// token (the shell honours the LAST assignment), so every `itervox action`
// call in the resumed session failed with unknown_token.

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

var actionTokenAssignment = regexp.MustCompile(`ITERVOX_ACTION_TOKEN='([^']*)'`)

// effectiveActionToken returns the token a shell would export for command:
// with repeated `VAR=value` prefixes the last assignment wins.
func effectiveActionToken(command string) string {
	matches := actionTokenAssignment.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// tokenCheckingRunner validates the effective action token against the grant
// store while the turn is live, mirroring what `itervox action comment` does.
type tokenCheckingRunner struct {
	store  *agentactions.Store
	result agent.TurnResult

	mu       sync.Mutex
	commands []string
	reasons  []string
}

func (r *tokenCheckingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	_, reason, _ := r.store.Validate(effectiveActionToken(command), "ENG-1", config.AgentActionComment, time.Now())
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.reasons = append(r.reasons, reason)
	r.mu.Unlock()
	return r.result, nil
}

func drainWorkerExit(t *testing.T, o *Orchestrator) OrchestratorEvent {
	t.Helper()
	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerExited {
				return ev
			}
		default:
			t.Fatal("runWorker returned without emitting EventWorkerExited")
			return OrchestratorEvent{}
		}
	}
}

func TestResumedInputRequiredRunGetsLiveActionToken(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	cfg.Agent.DefaultAllowedActions = []string{config.AgentActionComment}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	store := agentactions.NewStore()
	runner := &tokenCheckingRunner{
		store:  store,
		result: agent.TurnResult{InputRequired: true, ResultText: "Which environment should I deploy to?"},
	}
	o := New(cfg, mt, runner, nil)
	o.events = make(chan OrchestratorEvent, 64)
	o.SetAgentActionTokens(store)
	o.SetAgentActionBaseURL("http://127.0.0.1:1")

	// Run 1: asks for input; the worker exits and revokes its token.
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "claude", "claude", "", true, nil, nil, nil)
	ev := drainWorkerExit(t, o)
	require.NotNil(t, ev.InputRequiredEntry, "run 1 must park as input-required")
	require.Equal(t, []string{""}, runner.reasons, "run 1's own token must validate during its turn")

	stored := ev.InputRequiredEntry.Command
	assert.NotContains(t, stored, "ITERVOX_ACTION_TOKEN",
		"the persisted resume command must not carry the revoked run-scoped token")

	// Run 2: the human replied; resume with the persisted command.
	runner.result = agent.TurnResult{ResultText: "Deployed."}
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", stored, "claude", "", true,
		&ResumeContext{SessionID: "agent-session-1", UserMessage: "staging"}, nil, nil)
	drainWorkerExit(t, o)

	require.Len(t, runner.reasons, 2)
	assert.Empty(t, runner.reasons[1],
		"the resumed run's effective action token must be live, got %q for command %q",
		runner.reasons[1], runner.commands[1])
	assert.Len(t, actionTokenAssignment.FindAllString(runner.commands[1], -1), 1,
		"exactly one ITERVOX_ACTION_TOKEN assignment expected in the resumed command")
}
