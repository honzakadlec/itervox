package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const searchPageSize = 50

// jqlQuote wraps a value in double quotes for use in a JQL clause, escaping
// backslashes and embedded quotes.
func jqlQuote(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func jqlValueList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = jqlQuote(v)
	}
	return strings.Join(quoted, ",")
}

// FetchCandidateIssues returns issues in active states for the configured project.
// Empty ActiveStates returns an empty slice without any API call, matching the
// FetchIssuesByStates contract for "no states configured = no candidates".
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	if len(c.cfg.ActiveStates) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("project = %s AND status in (%s)", jqlQuote(c.cfg.ProjectSlug), jqlValueList(c.cfg.ActiveStates))
	return c.searchJQL(ctx, jql)
}

// FetchIssuesByStates returns issues matching the given state names.
// Empty stateNames returns an empty slice without any API call.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("project = %s AND status in (%s)", jqlQuote(c.cfg.ProjectSlug), jqlValueList(stateNames))
	return c.searchJQL(ctx, jql)
}

// FetchIssueStatesByIDs returns the current state snapshot for the given issue IDs.
// Empty issueIDs returns an empty slice without any API call.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("id in (%s)", jqlValueList(issueIDs))
	return c.searchJQL(ctx, jql)
}

// searchJQL runs a JQL search, paginating until every matching issue is
// fetched, then backfills blocker states across the full result set.
func (c *Client) searchJQL(ctx context.Context, jql string) ([]domain.Issue, error) {
	var all []domain.Issue
	startAt := 0
	for {
		path := fmt.Sprintf("/search?jql=%s&fields=%s&startAt=%d&maxResults=%d",
			url.QueryEscape(jql), url.QueryEscape(detailFields), startAt, searchPageSize)
		raw, err := c.doJSON(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("jira: search issues: %w", err)
		}
		rawIssues, _ := raw["issues"].([]any)
		for _, ri := range rawIssues {
			issueMap, ok := ri.(map[string]any)
			if !ok {
				continue
			}
			if issue := normalizeIssue(issueMap); issue != nil {
				all = append(all, *issue)
			}
		}
		total, _ := tracker.ToIntVal(raw["total"])
		startAt += len(rawIssues)
		if len(rawIssues) == 0 || startAt >= total {
			break
		}
	}
	return c.populateBlockerStates(ctx, all), nil
}
