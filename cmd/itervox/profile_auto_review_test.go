package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestOrchestratorAdapterUpsertProfile_PreservesAutoReview verifies a
// dashboard edit that does not send autoReview keeps the profile's
// auto_review: false (in memory and in WORKFLOW.md), while an explicit value
// from the request wins.
func TestOrchestratorAdapterUpsertProfile_PreservesAutoReview(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    tester:
      command: claude --model sonnet
      auto_review: false
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("tester", server.ProfileDef{Command: "claude --model opus", Enabled: true}, "tester"))
	assert.False(t, config.ProfileAutoReview(orch.ProfilesCfg()["tester"]), "edit without autoReview must keep auto_review: false")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_review: false")

	on := true
	require.NoError(t, adapter.UpsertProfile("tester", server.ProfileDef{Command: "claude --model opus", Enabled: true, AutoReview: &on}, "tester"))
	assert.True(t, config.ProfileAutoReview(orch.ProfilesCfg()["tester"]), "explicit autoReview must win")
	data, err = os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "auto_review:")

	defs := adapter.ProfileDefs()
	require.NotNil(t, defs["tester"].AutoReview)
	assert.True(t, *defs["tester"].AutoReview)
}

// TestOrchestratorAdapterUpsertProfile_PreservesSubAgents verifies a dashboard
// edit that does not send subAgents keeps sub_agents: false, while an explicit
// value from the request wins, and ProfileDefs reports it.
func TestOrchestratorAdapterUpsertProfile_PreservesSubAgents(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    tester:
      command: claude --model sonnet
      sub_agents: false
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}

	require.NoError(t, adapter.UpsertProfile("tester", server.ProfileDef{Command: "claude --model opus", Enabled: true}, "tester"))
	assert.False(t, config.ProfileSubAgents(orch.ProfilesCfg()["tester"]), "edit without subAgents must keep sub_agents: false")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "sub_agents: false")
	defs := adapter.ProfileDefs()
	require.NotNil(t, defs["tester"].SubAgents)
	assert.False(t, *defs["tester"].SubAgents)

	on := true
	require.NoError(t, adapter.UpsertProfile("tester", server.ProfileDef{Command: "claude --model opus", Enabled: true, SubAgents: &on}, "tester"))
	assert.True(t, config.ProfileSubAgents(orch.ProfilesCfg()["tester"]), "explicit subAgents must win")
	data, err = os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "sub_agents:")
}
