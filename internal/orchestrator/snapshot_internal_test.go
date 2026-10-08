package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// CORE-037: every orchestrator ledger write goes through atomicfs.WriteFile
// (temp + fsync + rename + best-effort parent-dir fsync). The test pins the
// observable contract on the real write path: an existing file is replaced
// whole, no temp file is left behind, and the per-call permission lands.
func TestWriteLedgerFileUsesAtomicfsReplaceWithoutTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input_required.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"old":true}`), 0o644))

	o := New(&config.Config{}, nil, nil, nil)
	require.NoError(t, o.writeLedgerFile(path, []byte(`{"new":true}`), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"new":true}`, string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "per-call perm must be preserved")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "input_required.json", entries[0].Name())
}

// F-1 propagation: AutomationID, TriggerType, and CommentCount must survive
// the deep-copy in storeSnap → Snapshot. Without these fields propagating, every
// later automation visualization (T-2 activity card, T-5 timeline filter,
// T-6 comment-count badge) would be impossible.
func TestSnapshotPreservesAutomationFieldsOnRunningEntries(t *testing.T) {
	o := New(&config.Config{}, nil, nil, nil)

	state := NewState(&config.Config{})
	state.Running["ENG-1"] = &RunEntry{
		Issue:        domain.Issue{ID: "u1", Identifier: "ENG-1", Title: "T", State: "In Progress"},
		Kind:         "automation",
		AutomationID: "pr-on-input",
		TriggerType:  "input_required",
		CommentCount: 3,
	}
	o.storeSnap(state)

	snap := o.Snapshot()
	require.Contains(t, snap.Running, "ENG-1")
	got := snap.Running["ENG-1"]
	assert.Equal(t, "pr-on-input", got.AutomationID)
	assert.Equal(t, "input_required", got.TriggerType)
	assert.Equal(t, 3, got.CommentCount)
	// Manual runs (no automation context) leave the fields zero-valued; the
	// JSON tag on server.RunningRow uses `omitempty` so they vanish from the
	// wire format. Verify we don't leak a default value.
	state2 := NewState(&config.Config{})
	state2.Running["ENG-2"] = &RunEntry{
		Issue: domain.Issue{ID: "u2", Identifier: "ENG-2", Title: "T2", State: "In Progress"},
	}
	o.storeSnap(state2)
	snap2 := o.Snapshot()
	require.Contains(t, snap2.Running, "ENG-2")
	manual := snap2.Running["ENG-2"]
	assert.Empty(t, manual.AutomationID)
	assert.Empty(t, manual.TriggerType)
	assert.Equal(t, 0, manual.CommentCount)
}

// TestInputRequiredDiskPersistsAutomationDispatch is a regression test for
// the DBIMPROVE-598 follow-up gap: saveInputRequiredToDisk/
// loadInputRequiredFromDisk used to round-trip only Kind/AutomationID/
// TriggerType, dropping the full *AutomationDispatch and RequiresMoveState.
// A daemon restart between "agent hit input-required" and "human replies"
// would silently lose that context on load, so the eventual resume (see
// processPendingInputResumes) would dispatch with automation=nil even though
// Kind still said "automation" — the same class of bug fixed in-memory by
// TestResumedAutomationRunCarriesFullAutomationDispatch, but reachable via a
// restart instead of a same-process resume.
func TestInputRequiredDiskPersistsAutomationDispatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input_required.json")

	o := New(&config.Config{}, nil, nil, nil)
	o.SetInputRequiredFile(path)

	automation := &AutomationDispatch{
		AutomationID: "deploy-check",
		ProfileName:  "deploy-checker",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerIssueEnteredState,
			TriggerState: "04-With Developer",
		},
	}
	awaiting := map[string]*InputRequiredEntry{
		"DBIMPROVE-598": {
			IssueID:           "id1",
			Identifier:        "DBIMPROVE-598",
			SessionID:         "s1",
			Context:           "Should I move to QA?",
			Backend:           "claude",
			Command:           "claude",
			QueuedAt:          time.Now(),
			Kind:              "automation",
			AutomationID:      "deploy-check",
			TriggerType:       string(config.AutomationTriggerIssueEnteredState),
			Automation:        automation,
			RequiresMoveState: true,
		},
	}
	pending := map[string]*PendingInputResumeEntry{
		"DBIMPROVE-599": {
			IssueID:           "id2",
			Identifier:        "DBIMPROVE-599",
			SessionID:         "s2",
			UserMessage:       "Approved.",
			QueuedAt:          time.Now(),
			Kind:              "automation",
			AutomationID:      "deploy-check",
			TriggerType:       string(config.AutomationTriggerIssueEnteredState),
			Automation:        automation,
			RequiresMoveState: true,
		},
	}
	o.saveInputRequiredToDisk(awaiting, pending)

	loaded := o.loadInputRequiredFromDisk(NewState(&config.Config{}))

	require.Contains(t, loaded.InputRequiredIssues, "DBIMPROVE-598")
	awaitingGot := loaded.InputRequiredIssues["DBIMPROVE-598"]
	require.NotNil(t, awaitingGot.Automation, "InputRequiredEntry.Automation must survive the disk round trip")
	assert.Equal(t, "deploy-check", awaitingGot.Automation.AutomationID)
	assert.Equal(t, "deploy-checker", awaitingGot.Automation.ProfileName)
	assert.True(t, awaitingGot.RequiresMoveState)

	require.Contains(t, loaded.PendingInputResumes, "DBIMPROVE-599")
	pendingGot := loaded.PendingInputResumes["DBIMPROVE-599"]
	require.NotNil(t, pendingGot.Automation, "PendingInputResumeEntry.Automation must survive the disk round trip")
	assert.Equal(t, "deploy-check", pendingGot.Automation.AutomationID)
	assert.True(t, pendingGot.RequiresMoveState)
}
