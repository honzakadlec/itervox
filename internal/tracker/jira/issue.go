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

// CreateComment posts a comment on the given Jira issue, wrapping body in a
// minimal single-paragraph ADF document (no markdown rendering).
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	reqBody := map[string]any{"body": adfDoc(body)}
	raw, err := c.doJSON(ctx, http.MethodPost, "/issue/"+url.PathEscape(issueID)+"/comment", reqBody)
	if err != nil {
		return nil, fmt.Errorf("jira: create comment on %s: %w", issueID, err)
	}
	id, _ := raw["id"].(string)
	author, _ := raw["author"].(map[string]any)
	authorID, _ := author["accountId"].(string)
	authorName, _ := author["displayName"].(string)
	return &domain.Comment{
		ID:         id,
		Body:       body,
		AuthorID:   authorID,
		AuthorName: authorName,
		CreatedAt:  parseJiraTime(raw["created"]),
	}, nil
}

// CreateIssue creates a follow-up Jira issue in the configured project.
// sourceIssueID is accepted for tracker interface parity but not otherwise used.
// When stateName is non-empty, the new issue is transitioned to that workflow
// status after creation (new Jira issues start in the project's default status).
func (c *Client) CreateIssue(ctx context.Context, _ string, title, body, stateName string) (*domain.Issue, error) {
	reqBody := map[string]any{
		"fields": map[string]any{
			"project":     map[string]any{"key": c.cfg.ProjectSlug},
			"summary":     title,
			"description": adfDoc(body),
			"issuetype":   map[string]any{"name": c.cfg.DefaultIssueType},
		},
	}
	raw, err := c.doJSON(ctx, http.MethodPost, "/issue", reqBody)
	if err != nil {
		return nil, fmt.Errorf("jira: create issue: %w", err)
	}
	key, _ := raw["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("jira: create issue: response missing key")
	}
	if stateName != "" {
		if err := c.UpdateIssueState(ctx, key, stateName); err != nil {
			return nil, fmt.Errorf("jira: create issue %s: set initial state: %w", key, err)
		}
	}
	return c.FetchIssueDetail(ctx, key)
}

// UpdateIssueState transitions the Jira issue to the named workflow status by
// looking up the transition whose target status matches stateName, then
// posting it. Jira transitions are workflow-specific IDs, not status names
// directly, so this always requires the GET-then-POST round trip.
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	raw, err := c.doJSON(ctx, http.MethodGet, "/issue/"+url.PathEscape(issueID)+"/transitions", nil)
	if err != nil {
		return fmt.Errorf("jira: fetch transitions for %s: %w", issueID, err)
	}
	rawTransitions, _ := raw["transitions"].([]any)
	transitionID := ""
	for _, rt := range rawTransitions {
		t, ok := rt.(map[string]any)
		if !ok {
			continue
		}
		to, _ := t["to"].(map[string]any)
		toName, _ := to["name"].(string)
		if strings.EqualFold(toName, stateName) {
			transitionID, _ = t["id"].(string)
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("jira: no transition to state %q for issue %s", stateName, issueID)
	}
	body := map[string]any{"transition": map[string]any{"id": transitionID}}
	if _, err := c.doJSON(ctx, http.MethodPost, "/issue/"+url.PathEscape(issueID)+"/transitions", body); err != nil {
		return fmt.Errorf("jira: transition %s to %q: %w", issueID, stateName, err)
	}
	return nil
}

// detailFields lists the Jira fields fetched for a full issue detail view.
const detailFields = "summary,description,status,issuetype,created,updated,issuelinks,comment"

// FetchIssueDetail returns a single Jira issue with full details including comments.
// issueID may be either the numeric Jira id or the issue key — Jira's REST API
// accepts both interchangeably at this endpoint.
func (c *Client) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	raw, err := c.doJSON(ctx, http.MethodGet, "/issue/"+url.PathEscape(issueID)+"?fields="+detailFields, nil)
	if err != nil {
		return nil, fmt.Errorf("jira: fetch issue detail %s: %w", issueID, err)
	}
	issue := normalizeIssue(raw)
	if issue == nil {
		return nil, &tracker.NotFoundError{Adapter: "jira", Identifier: issueID}
	}
	issues := c.populateBlockerStates(ctx, []domain.Issue{*issue})
	return &issues[0], nil
}

// FetchIssueByIdentifier returns a single Jira issue by its key (e.g. "PROJ-42").
// Jira's issue endpoint accepts the key directly, so this delegates to FetchIssueDetail.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	return c.FetchIssueDetail(ctx, identifier)
}

// SetIssueBranch records the feature branch name on the Jira issue via a
// marker comment (Jira's ADF format has no hidden-comment equivalent to
// GitHub's HTML-comment trick, so the marker is a plain, low-noise comment).
// scanBranchMarker restores it on subsequent fetches.
func (c *Client) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	if _, err := c.CreateComment(ctx, issueID, branchMarkerPrefix+branchName); err != nil {
		return fmt.Errorf("jira: set issue branch on %s: %w", issueID, err)
	}
	return nil
}
