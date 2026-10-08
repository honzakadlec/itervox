package gitlab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// FetchCandidateIssues fetches open issues in the configured active states.
// One request per active state (GitLab's labels filter is AND-semantics, and
// scoped labels only ever carry one value, so filtering by a single
// status::<state> label at a time and deduplicating by ID mirrors the
// approach used by the GitHub adapter).
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	seen := make(map[string]struct{})
	var all []domain.Issue
	for _, activeState := range c.cfg.ActiveStates {
		issues, err := c.fetchByLabel(ctx, activeState, "opened", c.cfg.ActiveStates)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if _, dup := seen[issue.ID]; !dup {
				seen[issue.ID] = struct{}{}
				all = append(all, issue)
			}
		}
	}
	all = c.populateLinkedBlockers(ctx, all)
	all = c.populateBlockerStates(ctx, all)
	return all, nil
}

// FetchIssuesByStates fetches issues matching the given itervox state names.
// Uses state=all (rather than guessing opened vs closed) because a terminal
// state's issue may or may not be natively closed depending on whether it is
// the configured CompletionState (see UpdateIssueState).
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	seen := make(map[string]struct{})
	var all []domain.Issue
	for _, state := range stateNames {
		issues, err := c.fetchByLabel(ctx, state, "all", stateNames)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if _, dup := seen[issue.ID]; !dup {
				seen[issue.ID] = struct{}{}
				all = append(all, issue)
			}
		}
	}
	all = c.populateLinkedBlockers(ctx, all)
	return c.populateBlockerStates(ctx, all), nil
}

// fetchByLabel fetches issues carrying the status::<stateName> label,
// filtered by native GitLab state ("opened"/"closed"/"all"), paginated.
// extraStates lists additional state names (e.g. backlog_states) accepted
// as a fallback when the derived state doesn't match active/terminal.
func (c *Client) fetchByLabel(ctx context.Context, stateName, glState string, extraStates []string) ([]domain.Issue, error) {
	q := url.Values{}
	q.Set("state", glState)
	q.Set("labels", statusLabel(stateName))
	q.Set("per_page", strconv.Itoa(pageSize))
	u := c.projectURL("/issues?" + q.Encode())
	return c.fetchPaginated(ctx, u, extraStates)
}

// maxConcurrentFetches caps concurrent goroutines in boundedDo.
const maxConcurrentFetches = 8

// boundedDo runs fn for each item in items with at most maxConcurrentFetches
// goroutines in flight simultaneously.
func boundedDo[T any](ctx context.Context, items []T, fn func(ctx context.Context, idx int, item T)) {
	sem := make(chan struct{}, maxConcurrentFetches)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it T) {
			defer func() { <-sem }()
			defer wg.Done()
			fn(ctx, i, it)
		}(i, item)
	}
	wg.Wait()
}

// FetchIssueStatesByIDs fetches each issue individually (GitLab has no batch endpoint).
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}

	type fetchResult struct {
		issue *domain.Issue
		err   error
		idx   int
	}

	ch := make(chan fetchResult, len(issueIDs))
	boundedDo(ctx, issueIDs, func(ctx context.Context, idx int, issueID string) {
		issue, err := c.fetchSingleIssue(ctx, issueID)
		ch <- fetchResult{issue: issue, err: err, idx: idx}
	})
	close(ch)

	issues := make([]domain.Issue, len(issueIDs))
	for r := range ch {
		if r.err != nil {
			if errors.Is(r.err, tracker.ErrNotFound) {
				continue // deleted issue — reconciler will stop the worker
			}
			return nil, r.err
		}
		if r.issue != nil {
			issues[r.idx] = *r.issue
		}
	}

	var out []domain.Issue
	for _, issue := range issues {
		if issue.ID != "" {
			out = append(out, issue)
		}
	}
	return out, nil
}

// fetchSingleIssue fetches one GitLab issue by its project-scoped IID.
func (c *Client) fetchSingleIssue(ctx context.Context, issueIID string) (*domain.Issue, error) {
	body, _, err := c.get(ctx, c.projectURL("/issues/"+url.PathEscape(issueIID)))
	if err != nil {
		return nil, err
	}
	raw, ok := body.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gitlab_unknown_payload: unexpected issue shape")
	}
	derived := c.issueState(raw)
	return normalizeIssue(raw, derived), nil
}

// fetchPaginated follows Link header pagination for a GitLab issues list endpoint.
func (c *Client) fetchPaginated(ctx context.Context, startURL string, extraStates []string) ([]domain.Issue, error) {
	var all []domain.Issue
	nextURL := startURL

	for nextURL != "" {
		body, linkHeader, err := c.get(ctx, nextURL)
		if err != nil {
			return nil, err
		}

		rawItems, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("gitlab_unknown_payload: expected array response")
		}

		for _, item := range rawItems {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			derived := c.issueState(raw)
			if derived == "" {
				for _, label := range extractLabels(raw) {
					value, hasScope := strings.CutPrefix(label, statusLabelPrefix)
					if !hasScope {
						continue
					}
					for _, extra := range extraStates {
						if strings.EqualFold(value, extra) {
							derived = extra
							break
						}
					}
					if derived != "" {
						break
					}
				}
			}
			if derived == "" {
				continue // not eligible
			}
			if issue := normalizeIssue(raw, derived); issue != nil {
				all = append(all, *issue)
			}
		}

		next, err := ParseNextLink(linkHeader)
		if err != nil {
			break // ErrMissingPageLink means the last page had no rel="next" — treat as done.
		}
		nextURL = next
	}

	return all, nil //nolint:nilerr // link-parse errors mean end of pagination, not a real error
}

// populateBlockerStates fetches the current state for each blocker referenced
// in issues and backfills BlockerRef.State. Refs that already carry a State
// (native issue links, see populateLinkedBlockers) are not re-fetched.
// Fail-safe: any fetch error leaves State nil so the orchestrator treats the
// dependency as unknown/unmet rather than assuming it's resolved.
func (c *Client) populateBlockerStates(ctx context.Context, issues []domain.Issue) []domain.Issue {
	seen := make(map[string]struct{})
	var ids []string
	for _, issue := range issues {
		for _, b := range issue.BlockedBy {
			if b.ID != nil && b.State == nil {
				if _, ok := seen[*b.ID]; !ok {
					seen[*b.ID] = struct{}{}
					ids = append(ids, *b.ID)
				}
			}
		}
	}
	if len(ids) == 0 {
		return issues
	}

	type result struct {
		id    string
		state string
		url   string
	}
	ch := make(chan result, len(ids))
	boundedDo(ctx, ids, func(ctx context.Context, _ int, id string) {
		issue, err := c.fetchSingleIssue(ctx, id)
		if err != nil {
			slog.Error("gitlab: blocker state fetch failed — dependents stay blocked until resolved",
				"blocker_id", id, "error", err)
			ch <- result{id: id}
			return
		}
		if issue == nil {
			ch <- result{id: id}
			return
		}
		u := ""
		if issue.URL != nil {
			u = *issue.URL
		}
		ch <- result{id: id, state: issue.State, url: u}
	})
	close(ch)

	resultMap := make(map[string]result, len(ids))
	for r := range ch {
		resultMap[r.id] = r
	}

	for i := range issues {
		for j := range issues[i].BlockedBy {
			if issues[i].BlockedBy[j].ID != nil {
				if issues[i].BlockedBy[j].State != nil {
					continue
				}
				if r, ok := resultMap[*issues[i].BlockedBy[j].ID]; ok {
					if r.state != "" {
						state := r.state
						issues[i].BlockedBy[j].State = &state
					}
					if r.url != "" {
						u := r.url
						issues[i].BlockedBy[j].URL = &u
					}
				}
			}
		}
	}
	return issues
}
