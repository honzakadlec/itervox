package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reviewer runs while the issue sits in review_state, outside ActiveStates.
// When it pauses for input there, the reply must resume it, not be dropped as
// stale.
func TestPendingInputResumeInReviewState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	a.State = "Review"
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	o.cfg.Tracker.ReviewState = "Review"
	state := NewState(o.cfg)
	state.PendingInputResumes[a.Identifier] = &PendingInputResumeEntry{
		IssueID:     a.ID,
		Identifier:  a.Identifier,
		SessionID:   "sess-1",
		Backend:     "claude",
		ProfileName: "reviewer",
		UserMessage: "go ahead",
	}

	state = o.processPendingInputResumes(ctx, state, time.Now())

	require.Contains(t, state.Running, a.ID, "the paused reviewer resumes")
	assert.Equal(t, "sess-1", state.Running[a.ID].AgentSessionID)
	cancel()
}

// The resumed reviewer must still be a reviewer, or reconcile stops it the
// moment it sees the issue in review_state.
func TestResumedReviewerSurvivesReconcile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	a.State = "Review"
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	o.cfg.Tracker.ReviewState = "Review"
	state := NewState(o.cfg)
	state.Running[a.ID] = &RunEntry{Issue: a, Kind: "reviewer", ProfileName: "reviewer", StartedAt: time.Now()}

	state = o.handleEvent(ctx, state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: a.ID,
		RunEntry: &RunEntry{
			Issue:          a,
			TerminalReason: TerminalInputRequired,
			StartedAt:      time.Now(),
		},
		InputRequiredEntry: &InputRequiredEntry{
			IssueID: a.ID, Identifier: a.Identifier, SessionID: "sess-1",
			Backend: "claude", ProfileName: "reviewer", QueuedAt: time.Now(),
		},
	})
	paused := state.InputRequiredIssues[a.Identifier]
	require.NotNil(t, paused)
	state.PendingInputResumes[a.Identifier] = buildPendingInputResumeEntry(paused, "go ahead")
	state = o.processPendingInputResumes(ctx, state, time.Now())
	require.Contains(t, state.Running, a.ID, "the paused reviewer resumes")

	events := make(chan OrchestratorEvent, 4)
	state = ReconcileTrackerStates(ctx, state, o.tracker, events, nil)

	assert.Contains(t, state.Running, a.ID, "reconcile keeps the resumed reviewer running")
	cancel()
}
