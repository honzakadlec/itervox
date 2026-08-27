package jira

import (
	"context"
	"net/http"
	"net/url"

	"github.com/vnovick/itervox/internal/domain"
)

// populateBlockerStates fetches the current state for each blocker referenced
// in issues and backfills BlockerRef.State. Error handling is fail-safe: any
// fetch error leaves State nil so the orchestrator treats the dependency as
// unknown (and keeps dependents blocked) rather than assuming it's resolved.
func (c *Client) populateBlockerStates(ctx context.Context, issues []domain.Issue) []domain.Issue {
	stateByID := make(map[string]string)
	for _, issue := range issues {
		for _, blocker := range issue.BlockedBy {
			if blocker.ID == nil {
				continue
			}
			if _, ok := stateByID[*blocker.ID]; ok {
				continue
			}
			state, ok := c.fetchBlockerState(ctx, *blocker.ID)
			if ok {
				stateByID[*blocker.ID] = state
			}
		}
	}
	for i := range issues {
		for j := range issues[i].BlockedBy {
			blocker := &issues[i].BlockedBy[j]
			if blocker.ID == nil {
				continue
			}
			if state, ok := stateByID[*blocker.ID]; ok {
				blocker.State = &state
			}
		}
	}
	return issues
}

func (c *Client) fetchBlockerState(ctx context.Context, blockerID string) (string, bool) {
	raw, err := c.doJSON(ctx, http.MethodGet, "/issue/"+url.PathEscape(blockerID)+"?fields=status", nil)
	if err != nil {
		return "", false
	}
	fields, _ := raw["fields"].(map[string]any)
	name := statusName(fields)
	if name == "" {
		return "", false
	}
	return name, true
}
