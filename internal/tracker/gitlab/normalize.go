package gitlab

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// normalizeIssue converts a raw GitLab REST API issue map to a domain.Issue.
// derivedState is the computed state string (from the status::* scoped label
// plus native opened/closed logic). Returns nil if required fields are missing.
// Uses the project-scoped "iid" (not the global "id") as the domain ID, so
// FetchIssueByIdentifier's "#42" format round-trips directly.
func normalizeIssue(raw map[string]any, derivedState string) *domain.Issue {
	iidRaw, ok := raw["iid"]
	if !ok {
		return nil
	}
	iid, ok := tracker.ToIntVal(iidRaw)
	if !ok {
		return nil
	}
	title, _ := raw["title"].(string)
	if title == "" {
		return nil
	}

	id := strconv.Itoa(iid)
	identifier := fmt.Sprintf("#%d", iid)

	issue := &domain.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      title,
		State:      derivedState,
		Labels:     extractLabels(raw),
		BlockedBy:  extractBlockers(raw),
		CreatedAt:  tracker.ParseTime(raw["created_at"]),
		UpdatedAt:  tracker.ParseTime(raw["updated_at"]),
	}

	if desc, ok := raw["description"].(string); ok && desc != "" {
		issue.Description = &desc
	}
	if webURL, ok := raw["web_url"].(string); ok && webURL != "" {
		issue.URL = &webURL
	}
	if prio := priorityFromLabels(issue.Labels); prio >= 0 {
		issue.Priority = &prio
	}
	// branch_name: populated separately by scanning notes for the itervox marker.
	issue.BranchName = nil

	return issue
}

func extractLabels(raw map[string]any) []string {
	labelsRaw, ok := raw["labels"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(labelsRaw))
	for _, l := range labelsRaw {
		name, ok := l.(string)
		if !ok || name == "" {
			continue
		}
		result = append(result, strings.ToLower(name))
	}
	return result
}

// statusScopeValue returns the value of the first "status::*" scoped label
// found (case preserved), or "" if none is present.
func statusScopeValue(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, statusLabelPrefix); ok {
			return v
		}
	}
	return ""
}

func extractBlockers(raw map[string]any) []domain.BlockerRef {
	body, ok := raw["description"].(string)
	if !ok || body == "" {
		return nil
	}
	nums := tracker.ParseHashBlockers(body)
	result := make([]domain.BlockerRef, 0, len(nums))
	for _, num := range nums {
		id := num
		ident := "#" + num
		result = append(result, domain.BlockerRef{
			ID:         &id,
			Identifier: &ident,
		})
	}
	return result
}

func priorityFromLabels(labels []string) int {
	for _, l := range labels {
		switch l {
		case "p0":
			return 0
		case "p1":
			return 1
		case "p2":
			return 2
		case "p3":
			return 3
		}
	}
	return -1
}

// deriveState computes the itervox state string for a GitLab issue from its
// status::* scoped label and native opened/closed state.
// Closed issues: prefer a matching terminal label if present, otherwise return
// the first configured terminal state (so the reconciler treats it as terminal
// regardless of which label — or lack of one — the issue carries).
// Open issues: first matching active or terminal label wins; "" if none match.
func deriveState(raw map[string]any, activeStates, terminalStates []string) string {
	glState, _ := raw["state"].(string)
	scoped := statusScopeValue(extractLabels(raw))
	if strings.EqualFold(glState, "closed") {
		for _, terminal := range terminalStates {
			if strings.EqualFold(scoped, terminal) {
				return terminal
			}
		}
		if len(terminalStates) > 0 {
			return terminalStates[0]
		}
		return "closed"
	}
	for _, active := range activeStates {
		if strings.EqualFold(scoped, active) {
			return active
		}
	}
	for _, terminal := range terminalStates {
		if strings.EqualFold(scoped, terminal) {
			return terminal
		}
	}
	return ""
}

// issueState derives the itervox state for raw using the client's configured
// states. An open issue labelled status::<ReviewState> maps to ReviewState so
// it shows as such and — being non-terminal — keeps its dependents blocked.
func (c *Client) issueState(raw map[string]any) string {
	derived := deriveState(raw, c.cfg.ActiveStates, c.cfg.TerminalStates)
	if derived != "" || c.cfg.ReviewState == "" {
		return derived
	}
	if glState, _ := raw["state"].(string); strings.EqualFold(glState, "closed") {
		return derived
	}
	if strings.EqualFold(statusScopeValue(extractLabels(raw)), c.cfg.ReviewState) {
		return c.cfg.ReviewState
	}
	return ""
}
