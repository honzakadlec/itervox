package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
)

// The dashboard shows the profile an issue actually dispatches with: the
// manual override, else the running run's profile, else a profile::<name>
// label naming an existing, enabled profile.
func TestEnrichIssue_AgentProfileShowsEffectiveProfile(t *testing.T) {
	off := false
	profiles := map[string]config.AgentProfile{
		"tester":      {Command: "claude"},
		"implementer": {Command: "claude"},
		"retired":     {Command: "claude", Enabled: &off},
	}
	now := time.Now()

	t.Run("label routes a queued issue", func(t *testing.T) {
		issue := baseIssue()
		issue.Labels = []string{"profile::tester", "status::todo"}
		assert.Equal(t, "tester", EnrichIssue(issue, emptyState(), now, baseCfg(), profiles).AgentProfile)
	})

	t.Run("override beats label", func(t *testing.T) {
		issue := baseIssue()
		issue.Labels = []string{"profile::tester"}
		snap := emptyState()
		snap.IssueProfiles["ENG-42"] = "implementer"
		assert.Equal(t, "implementer", EnrichIssue(issue, snap, now, baseCfg(), profiles).AgentProfile)
	})

	t.Run("running run's profile beats label", func(t *testing.T) {
		issue := baseIssue()
		issue.Labels = []string{"profile::tester"}
		snap := emptyState()
		snap.Running["uuid-1"] = &orchestrator.RunEntry{ProfileName: "implementer", StartedAt: now}
		assert.Equal(t, "implementer", EnrichIssue(issue, snap, now, baseCfg(), profiles).AgentProfile)
	})

	t.Run("running run with label profile", func(t *testing.T) {
		issue := baseIssue()
		issue.Labels = []string{"profile::tester"}
		snap := emptyState()
		snap.Running["uuid-1"] = &orchestrator.RunEntry{ProfileName: "tester", StartedAt: now}
		assert.Equal(t, "tester", EnrichIssue(issue, snap, now, baseCfg(), profiles).AgentProfile)
	})

	t.Run("unknown or disabled label profile shows nothing", func(t *testing.T) {
		issue := baseIssue()
		issue.Labels = []string{"profile::nope", "profile::retired"}
		assert.Empty(t, EnrichIssue(issue, emptyState(), now, baseCfg(), profiles).AgentProfile)
	})
}
