package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
)

func TestTurnOutcome(t *testing.T) {
	base := agent.TurnResult{InputTokens: 10, OutputTokens: 5, ResultText: "done"}
	with := func(f func(*agent.TurnResult)) agent.TurnResult {
		r := base
		f(&r)
		return r
	}
	tests := []struct {
		name     string
		result   agent.TurnResult
		runErr   error
		turn     int
		maxTurns int
		want     string
	}{
		{"clean mid-run turn", base, nil, 1, 3, "ok"},
		{"clean final turn", base, nil, 3, 3, "last_turn"},
		{"single-turn run", base, nil, 1, 1, "last_turn"},
		{"failed result", with(func(r *agent.TurnResult) { r.Failed = true; r.FailureText = "boom" }), nil, 1, 3, "failed"},
		{"runner error", base, errors.New("read timeout"), 1, 3, "failed"},
		{"runner error on final turn stays failed", base, errors.New("x"), 3, 3, "failed"},
		{"cancellation", base, context.Canceled, 1, 3, "failed"},
		{"input required", with(func(r *agent.TurnResult) { r.InputRequired = true }), nil, 1, 3, "input_required"},
		{"input required on final turn", with(func(r *agent.TurnResult) { r.InputRequired = true }), nil, 3, 3, "input_required"},
		{"vendor input request also sets Failed", with(func(r *agent.TurnResult) { r.InputRequired = true; r.Failed = true; r.FailureText = "q" }), nil, 1, 3, "input_required"},
		{"input required with runner error is a failure", with(func(r *agent.TurnResult) { r.InputRequired = true }), errors.New("x"), 1, 3, "failed"},
		{"quota limit beats input required", with(func(r *agent.TurnResult) {
			r.InputRequired = true
			r.Failed = true
			r.LastLimit = &agent.LimitSignal{Kind: agent.LimitKindQuota}
		}), nil, 1, 3, "failed"},
		{"sentinel in result text", with(func(r *agent.TurnResult) { r.ResultText = "which db?\n" + agent.InputRequiredSentinel }), nil, 1, 3, "input_required"},
		{"empty 0-token error is a concluded session", agent.TurnResult{Failed: true}, nil, 1, 3, "last_turn"},
		{"0-token clean turn ends the loop", agent.TurnResult{ResultText: "x"}, nil, 1, 3, "last_turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, turnOutcome(tt.result, tt.runErr, tt.turn, tt.maxTurns))
		})
	}
}

func TestHookRunEnv_TurnOutcomeOnlyWhenSet(t *testing.T) {
	assert.NotContains(t, hookRunEnv("ENG-1", "run-1", "p", ""), "ITERVOX_TURN_OUTCOME")
	assert.Equal(t, "failed", hookRunEnv("ENG-1", "run-1", "p", "failed")["ITERVOX_TURN_OUTCOME"])
}
