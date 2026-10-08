package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIssueProfileForDispatchFallsBackToDefaultProfile(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.DefaultProfile = "implementer"
	state := NewState(cfg)
	o := &Orchestrator{
		cfg:           cfg,
		issueProfiles: map[string]string{},
		issueBackends: map[string]string{},
	}

	assert.Equal(t, "implementer", o.issueProfileForDispatch(state, "ENG-1", nil),
		"issue with no override must use agent.default_profile")

	state.IssueProfiles["ENG-1"] = "codex-coder"
	assert.Equal(t, "codex-coder", o.issueProfileForDispatch(state, "ENG-1", nil),
		"auto-switch state must take precedence over the default")

	o.issueProfiles["ENG-1"] = "reviewer"
	assert.Equal(t, "reviewer", o.issueProfileForDispatch(state, "ENG-1", nil),
		"operator/reviewer overrides must take precedence over the default")
}

func TestIssueProfileForDispatchEmptyWithoutDefaultProfile(t *testing.T) {
	cfg := automationBaseCfg()
	state := NewState(cfg)
	o := &Orchestrator{
		cfg:           cfg,
		issueProfiles: map[string]string{},
		issueBackends: map[string]string{},
	}

	assert.Empty(t, o.issueProfileForDispatch(state, "ENG-1", nil))
}
