package gitlab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// linkTypeIsBlockedBy is the link_type GitLab reports on the blocked side of
// a "blocks" issue link. The inverse ("blocks") is ignored — the blocked
// issue always sees its own is_blocked_by entry.
const linkTypeIsBlockedBy = "is_blocked_by"

// linksUnavailableIdentifier marks a placeholder blocker added when an
// issue's links could not be fetched. It has no State, so the orchestrator
// keeps the issue held until a later poll fetches the links successfully.
const linksUnavailableIdentifier = "gitlab:links-unavailable"

// populateLinkedBlockers merges GitLab native "is blocked by" issue links
// into each non-terminal issue's BlockedBy. Blocker state is derived from the
// link payload itself, so linked blockers need no extra per-blocker fetch.
// Terminal issues are skipped — their blockers no longer affect dispatch.
func (c *Client) populateLinkedBlockers(ctx context.Context, issues []domain.Issue) []domain.Issue {
	var idxs []int
	for i, issue := range issues {
		if !c.isTerminal(issue.State) {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return issues
	}

	type result struct {
		idx  int
		refs []domain.BlockerRef
		err  error
	}
	ch := make(chan result, len(idxs))
	boundedDo(ctx, idxs, func(ctx context.Context, _ int, idx int) {
		refs, err := c.fetchLinkedBlockers(ctx, issues[idx].ID)
		ch <- result{idx: idx, refs: refs, err: err}
	})
	close(ch)

	for r := range ch {
		if r.err != nil {
			if errors.Is(r.err, tracker.ErrNotFound) {
				continue // issue vanished between list and links fetch
			}
			slog.Error("gitlab: issue links fetch failed — issue stays blocked until links resolve",
				"issue_id", issues[r.idx].ID, "error", r.err)
			ident := linksUnavailableIdentifier
			issues[r.idx].BlockedBy = append(issues[r.idx].BlockedBy, domain.BlockerRef{Identifier: &ident})
			continue
		}
		issues[r.idx].BlockedBy = mergeBlockers(issues[r.idx].BlockedBy, r.refs)
	}
	return issues
}

// fetchLinkedBlockers returns the is_blocked_by links of one issue.
// The links endpoint is not paginated.
func (c *Client) fetchLinkedBlockers(ctx context.Context, issueIID string) ([]domain.BlockerRef, error) {
	body, _, err := c.get(ctx, c.projectURL("/issues/"+url.PathEscape(issueIID)+"/links"))
	if err != nil {
		return nil, err
	}
	rawLinks, ok := body.([]any)
	if !ok {
		return nil, fmt.Errorf("gitlab_unknown_payload: expected array response for issue links")
	}
	var refs []domain.BlockerRef
	for _, item := range rawLinks {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if linkType, _ := raw["link_type"].(string); linkType != linkTypeIsBlockedBy {
			continue
		}
		if ref, ok := c.linkBlockerRef(raw); ok {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// linkBlockerRef converts one links-endpoint entry to a BlockerRef.
// Same-project blockers (relative reference "#N") use the iid as ID so they
// dedupe with "Blocked by #N" description refs and match snapshot issue IDs.
// Cross-project blockers use the full reference (e.g. "group/other#7") as
// both ID and Identifier, which can never collide with a numeric iid.
func (c *Client) linkBlockerRef(raw map[string]any) (domain.BlockerRef, bool) {
	iid, ok := tracker.ToIntVal(raw["iid"])
	if !ok {
		return domain.BlockerRef{}, false
	}
	id := strconv.Itoa(iid)
	ident := "#" + id
	if refs, ok := raw["references"].(map[string]any); ok {
		relative, _ := refs["relative"].(string)
		full, _ := refs["full"].(string)
		if relative != "" && !strings.HasPrefix(relative, "#") && full != "" {
			id, ident = full, full
		}
	}

	// Open blockers without a status::* label derive to "" — fall back to the
	// native state so State is always set (non-terminal → still blocking) and
	// populateBlockerStates never re-fetches link-sourced refs.
	state := c.issueState(raw)
	if state == "" {
		state, _ = raw["state"].(string)
	}
	ref := domain.BlockerRef{ID: &id, Identifier: &ident}
	if state != "" {
		ref.State = &state
	}
	if webURL, ok := raw["web_url"].(string); ok && webURL != "" {
		ref.URL = &webURL
	}
	return ref, true
}

// mergeBlockers merges link-derived refs into existing (description-derived)
// refs. A link ref with the same ID replaces the existing entry, since it
// carries the blocker's live state; new link refs are appended.
func mergeBlockers(existing, linked []domain.BlockerRef) []domain.BlockerRef {
	if len(linked) == 0 {
		return existing
	}
	pos := make(map[string]int, len(existing))
	for i, b := range existing {
		if b.ID != nil {
			pos[*b.ID] = i
		}
	}
	for _, l := range linked {
		if i, ok := pos[*l.ID]; ok {
			existing[i] = l
			continue
		}
		pos[*l.ID] = len(existing)
		existing = append(existing, l)
	}
	return existing
}

// isTerminal reports whether state is one of the configured terminal states.
func (c *Client) isTerminal(state string) bool {
	for _, t := range c.cfg.TerminalStates {
		if strings.EqualFold(state, t) {
			return true
		}
	}
	return false
}
