package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/config"
)

func profileLabelTestOrchestrator() (*Orchestrator, State) {
	cfg := automationBaseCfg()
	cfg.Agent.DefaultProfile = "implementer"
	disabled := false
	cfg.Agent.Profiles["implementer"] = config.AgentProfile{}
	cfg.Agent.Profiles["Tester"] = config.AgentProfile{}
	cfg.Agent.Profiles["retired"] = config.AgentProfile{Enabled: &disabled}
	o := &Orchestrator{
		cfg:           cfg,
		issueProfiles: map[string]string{},
		issueBackends: map[string]string{},
	}
	return o, NewState(cfg)
}

func TestIssueProfileForDispatch_LabelSelectsProfile(t *testing.T) {
	o, state := profileLabelTestOrchestrator()

	// Trackers lower-case labels (GitLab); profile names keep their case.
	got := o.issueProfileForDispatch(state, "ENG-1", []string{"bug", "profile::tester"})
	assert.Equal(t, "Tester", got, "profile::<name> label must select that profile")

	assert.Equal(t, "implementer", o.issueProfileForDispatch(state, "ENG-1", []string{"bug"}),
		"issue without a profile label must use agent.default_profile")
}

func TestIssueProfileForDispatch_OverrideBeatsLabel(t *testing.T) {
	o, state := profileLabelTestOrchestrator()
	labels := []string{"profile::tester"}

	state.IssueProfiles["ENG-1"] = "responder"
	assert.Equal(t, "responder", o.issueProfileForDispatch(state, "ENG-1", labels),
		"auto-switched/persisted per-issue profile must beat the label")

	o.issueProfiles["ENG-1"] = "default"
	assert.Equal(t, "default", o.issueProfileForDispatch(state, "ENG-1", labels),
		"operator/reviewer override must beat the label")
}

func TestIssueProfileForDispatch_UnknownLabelProfileFallsBack(t *testing.T) {
	o, state := profileLabelTestOrchestrator()

	assert.Equal(t, "implementer", o.issueProfileForDispatch(state, "ENG-1", []string{"profile::nope"}),
		"unknown label profile must fall back to agent.default_profile")
	assert.Equal(t, "Tester", o.issueProfileForDispatch(state, "ENG-1", []string{"profile::nope", "profile::tester"}),
		"first label naming a usable profile wins")
}

func TestIssueProfileForDispatch_DisabledLabelProfileFallsBack(t *testing.T) {
	o, state := profileLabelTestOrchestrator()

	assert.Equal(t, "implementer", o.issueProfileForDispatch(state, "ENG-1", []string{"profile::retired"}),
		"disabled label profile must fall back to agent.default_profile")
}

func TestHookRunEnv_IncludesProfile(t *testing.T) {
	env := hookRunEnv("ENG-1", "run-1", "tester")
	assert.Equal(t, map[string]string{
		"ITERVOX_ISSUE_IDENTIFIER": "ENG-1",
		"ITERVOX_RUN_ID":           "run-1",
		"ITERVOX_PROFILE":          "tester",
	}, env)
}
