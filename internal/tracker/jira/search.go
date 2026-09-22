package jira

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
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

// projectSlugs splits the configured ProjectSlug on commas, trimming
// whitespace around each entry. Supports both a single project key
// ("PROJ") and a comma-separated list ("PROJ,OTHER") for multi-project
// workflows.
func (c *Client) projectSlugs() []string {
	parts := strings.Split(c.cfg.ProjectSlug, ",")
	slugs := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			slugs = append(slugs, trimmed)
		}
	}
	return slugs
}

// jqlProjectClause builds the JQL project-scoping clause. Two or more
// configured slugs become "project in (X,Y)"; zero or one slug reproduces
// the historical "project = X" form verbatim (including the single-quoted
// empty string when ProjectSlug is unset), so existing single-project
// configs see no JQL change.
func jqlProjectClause(rawProjectSlug string, slugs []string) string {
	if len(slugs) > 1 {
		return "project in (" + jqlValueList(slugs) + ")"
	}
	return "project = " + jqlQuote(rawProjectSlug)
}

// FetchCandidateIssues returns issues in active states for the configured
// project(s). Empty ActiveStates returns an empty slice without any API
// call, matching the FetchIssuesByStates contract for "no states configured
// = no candidates".
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	if len(c.cfg.ActiveStates) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("%s AND status in (%s)", jqlProjectClause(c.cfg.ProjectSlug, c.projectSlugs()), jqlValueList(c.cfg.ActiveStates))
	return c.searchJQL(ctx, jql)
}

// FetchIssuesByStates returns issues matching the given state names.
// Empty stateNames returns an empty slice without any API call.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}
	jql := fmt.Sprintf("%s AND status in (%s)", jqlProjectClause(c.cfg.ProjectSlug, c.projectSlugs()), jqlValueList(stateNames))
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

// searchJQL runs a JQL search against the /search/jql endpoint, paginating
// via nextPageToken until every matching issue is fetched, then backfills
// blocker states across the full result set.
//
// Jira Cloud retired the legacy GET /search endpoint (startAt/total-based
// pagination) in 2025 — it now returns 410 Gone. /search/jql is the
// replacement: POST body, token-based pagination, no total count.
func (c *Client) searchJQL(ctx context.Context, jql string) ([]domain.Issue, error) {
	var all []domain.Issue
	nextPageToken := ""
	for {
		body := map[string]any{
			"jql":        jql,
			"fields":     strings.Split(detailFields, ","),
			"maxResults": searchPageSize,
		}
		if nextPageToken != "" {
			body["nextPageToken"] = nextPageToken
		}
		raw, err := c.doJSON(ctx, http.MethodPost, "/search/jql", body)
		if err != nil {
			return nil, fmt.Errorf("jira: search issues: %w", err)
		}
		rawIssues, _ := raw["issues"].([]any)
		for _, ri := range rawIssues {
			issueMap, ok := ri.(map[string]any)
			if !ok {
				continue
			}
			if issue := normalizeIssue(issueMap, c.cfg.Endpoint); issue != nil {
				all = append(all, *issue)
			}
		}
		isLast, _ := raw["isLast"].(bool)
		token, _ := raw["nextPageToken"].(string)
		if isLast || token == "" || len(rawIssues) == 0 {
			break
		}
		nextPageToken = token
	}
	return c.populateBlockerStates(ctx, all), nil
}
