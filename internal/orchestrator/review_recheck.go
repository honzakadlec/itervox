package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// Re-review of issues parked in tracker.review_state.
//
// A reviewer that cannot merge yet (a "Merge after" blocker is still open, or
// CI failed on a base the integration branch has since fixed) exits leaving
// the issue in review_state. review_state is not an active state, so polling
// never dispatches it again, and auto-review only fires after an implementer
// run: the issue sat there until an operator re-ran the review by hand.
//
// The event that usually unsticks those reviews is a sibling landing — its
// review_state → completion_state transition (mark_merged or a dashboard
// move) means the shared integration branch moved and any merge-order
// blocker naming it is now merged. noteReviewLanded flags that; the next tick
// re-queues every other review_state issue as a PendingReview, so the
// existing slot-aware, persisted resumePendingReviews path dispatches them.
// Merges are finite, so this cannot loop: an issue is re-reviewed at most
// once per sibling landing.

// noteReviewLanded flags a parked-review recheck when change is a
// review_state → completion_state transition. Event loop only.
func (o *Orchestrator) noteReviewLanded(state *State, change IssueStatusChange) {
	// ReviewState is read-only after startup — no lock required.
	reviewState := o.cfg.Tracker.ReviewState
	_, _, completion := o.TrackerStatesCfg()
	if reviewState == "" || completion == "" {
		return
	}
	if !strings.EqualFold(change.FromState, reviewState) || !strings.EqualFold(change.ToState, completion) {
		return
	}
	state.ReviewRecheckPending = true
}

// requeueParkedReviews records a PendingReview for every issue in
// review_state that is not already running, claimed, or pending, when a
// recheck is flagged. A failed fetch keeps the flag so the next tick retries.
// Event loop only.
func (o *Orchestrator) requeueParkedReviews(ctx context.Context, state *State, now time.Time) {
	if !state.ReviewRecheckPending {
		return
	}
	reviewState := o.cfg.Tracker.ReviewState
	_, autoReview := o.ReviewerCfg()
	chain := o.reviewerChainCfg()
	if reviewState == "" || !autoReview || len(chain) == 0 {
		state.ReviewRecheckPending = false
		return
	}
	issues, err := o.tracker.FetchIssuesByStates(ctx, []string{reviewState})
	if err != nil {
		slog.Warn("orchestrator: parked review recheck: fetch failed, retrying next tick", "error", err)
		return
	}
	state.ReviewRecheckPending = false
	o.cfgMu.RLock()
	requiredLabel := o.cfg.Tracker.RequiredLabel
	o.cfgMu.RUnlock()
	for _, issue := range issues {
		if !strings.EqualFold(issue.State, reviewState) {
			continue
		}
		// The fetch spans the whole project; daemons sharing it split it by
		// required_label, so another daemon's parked reviews are not ours.
		if requiredLabel != "" && !hasLabelFold(issue.Labels, requiredLabel) {
			continue
		}
		if _, running := state.Running[issue.ID]; running {
			continue
		}
		if _, claimed := state.Claimed[issue.ID]; claimed {
			continue
		}
		if _, pending := state.PendingReviews[issue.Identifier]; pending {
			continue
		}
		// A profile that opted out of auto-review (e.g. a release merger)
		// did not hand its issue to the reviewer in the first place.
		profileName := o.issueProfileForDispatch(*state, issue.Identifier, issue.Labels)
		o.cfgMu.RLock()
		p, ok := o.cfg.Agent.Profiles[profileName]
		o.cfgMu.RUnlock()
		if ok && !config.ProfileAutoReview(p) {
			continue
		}
		slog.Info("orchestrator: re-queueing parked review after a sibling landed",
			"identifier", issue.Identifier, "profile", chain[0])
		o.recordPendingReview(state, issue, chain[0], now)
	}
}
