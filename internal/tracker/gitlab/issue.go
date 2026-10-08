package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// itervoxBranchPrefix embeds the branch name in a hidden HTML comment so it
// survives round-trips without polluting the issue's rendered note thread.
// Matches the GitHub adapter's exact marker format for consistency.
const itervoxBranchPrefix = "<!-- itervox:branch:"

// FetchIssueDetail returns a single issue with its full comment (note) thread.
// issueID is the project-scoped IID as a string (e.g. "42").
func (c *Client) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	body, _, err := c.get(ctx, c.projectURL("/issues/"+url.PathEscape(issueID)))
	if err != nil {
		return nil, fmt.Errorf("gitlab_fetch_issue_detail: %w", err)
	}
	raw, ok := body.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gitlab_fetch_issue_detail: unexpected issue shape")
	}
	derived := c.issueState(raw)
	issue := normalizeIssue(raw, derived)
	if issue == nil {
		return nil, &tracker.NotFoundError{Adapter: "gitlab", Identifier: issueID}
	}

	notesURL := c.projectURL(fmt.Sprintf("/issues/%s/notes?order_by=created_at&sort=asc&per_page=%d", url.PathEscape(issueID), pageSize))
	for notesURL != "" {
		notesBody, linkHeader, err := c.get(ctx, notesURL)
		if err != nil {
			break // non-fatal: return the issue without comments rather than failing entirely.
		}
		rawNotes, ok := notesBody.([]any)
		if !ok {
			break
		}
		for _, item := range rawNotes {
			note, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if system, _ := note["system"].(bool); system {
				continue // skip GitLab's auto-generated activity notes (label changes, etc.)
			}
			noteBody, _ := note["body"].(string)
			if noteBody == "" {
				continue
			}
			if branch, ok := strings.CutPrefix(noteBody, itervoxBranchPrefix); ok {
				branch = strings.TrimSuffix(strings.TrimSpace(branch), "-->")
				branch = strings.TrimSpace(branch)
				if branch != "" {
					b := branch
					issue.BranchName = &b // last marker wins (most recent)
				}
				continue
			}
			var authorID, authorName string
			if author, ok := note["author"].(map[string]any); ok {
				if id, ok := tracker.ToIntVal(author["id"]); ok {
					authorID = strconv.Itoa(id)
				}
				authorName, _ = author["username"].(string)
			}
			noteID := ""
			if id, ok := tracker.ToIntVal(note["id"]); ok {
				noteID = strconv.Itoa(id)
			}
			issue.Comments = append(issue.Comments, domain.Comment{
				ID:         noteID,
				Body:       noteBody,
				CreatedAt:  tracker.ParseTime(note["created_at"]),
				AuthorID:   authorID,
				AuthorName: authorName,
			})
		}
		next, err := ParseNextLink(linkHeader)
		if err != nil || next == "" {
			break
		}
		notesURL = next
	}

	populated := c.populateBlockerStates(ctx, []domain.Issue{*issue})
	return &populated[0], nil
}

// FetchIssueByIdentifier returns a single issue by its human-readable
// identifier (e.g. "#42"). The leading "#" is stripped before calling FetchIssueDetail.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	issueID := strings.TrimPrefix(identifier, "#")
	return c.FetchIssueDetail(ctx, issueID)
}

// CreateComment posts a note on the GitLab issue identified by issueID
// (the project-scoped IID, as a string).
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	raw, err := c.doJSON(ctx, http.MethodPost, c.projectURL("/issues/"+url.PathEscape(issueID)+"/notes"), map[string]string{"body": body})
	if err != nil {
		return nil, fmt.Errorf("gitlab_create_comment: %w", err)
	}
	comment := &domain.Comment{Body: body}
	if id, ok := tracker.ToIntVal(raw["id"]); ok {
		comment.ID = strconv.Itoa(id)
	}
	comment.CreatedAt = tracker.ParseTime(raw["created_at"])
	if author, ok := raw["author"].(map[string]any); ok {
		if id, ok := tracker.ToIntVal(author["id"]); ok {
			comment.AuthorID = strconv.Itoa(id)
		}
		comment.AuthorName, _ = author["username"].(string)
	}
	if postedBody, ok := raw["body"].(string); ok && postedBody != "" {
		comment.Body = postedBody
	}
	return comment, nil
}

// CreateIssue creates a new issue in the configured GitLab project. The
// sourceIssueID is accepted for tracker interface parity but not otherwise
// used — GitLab adapter is single-project scoped, like GitHub's.
func (c *Client) CreateIssue(ctx context.Context, _ string, title, body, stateName string) (*domain.Issue, error) {
	payload := map[string]any{
		"title":       title,
		"description": body,
	}
	if stateName = strings.TrimSpace(stateName); stateName != "" {
		payload["labels"] = statusLabel(stateName)
	}
	raw, err := c.doJSON(ctx, http.MethodPost, c.projectURL("/issues"), payload)
	if err != nil {
		return nil, fmt.Errorf("gitlab_create_issue: %w", err)
	}
	derived := c.issueState(raw)
	if derived == "" {
		derived = stateName
	}
	issue := normalizeIssue(raw, derived)
	if issue == nil {
		return nil, fmt.Errorf("gitlab_create_issue: missing issue fields in response")
	}
	return issue, nil
}

// UpdateIssueState transitions the issue by setting its status::<stateName>
// scoped label. GitLab permits only one label per scope on an issue at a
// time, so add_labels alone clears any previous status::* label without an
// explicit remove step.
//
// When stateName matches the configured CompletionState, the issue is also
// natively closed (state_event: close) — other terminal states (e.g. a
// failed state) leave the issue open. When stateName is one of the
// configured ActiveStates, the issue is reopened if it was previously closed
// (state_event: reopen; a no-op on GitLab's side if already open), so a
// retried issue that had reached CompletionState doesn't get stuck closed.
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	payload := map[string]any{"add_labels": statusLabel(stateName)}
	switch {
	case c.cfg.CompletionState != "" && strings.EqualFold(stateName, c.cfg.CompletionState):
		payload["state_event"] = "close"
	default:
		for _, active := range c.cfg.ActiveStates {
			if strings.EqualFold(stateName, active) {
				payload["state_event"] = "reopen"
				break
			}
		}
	}
	if _, err := c.doJSON(ctx, http.MethodPut, c.projectURL("/issues/"+url.PathEscape(issueID)), payload); err != nil {
		return fmt.Errorf("gitlab_update_state: %w", err)
	}
	return nil
}

// SetIssueBranch posts a hidden HTML comment recording the branch name on the
// GitLab issue. FetchIssueDetail scans for this marker to restore BranchName
// on subsequent fetches, enabling retried workers to resume the correct branch.
func (c *Client) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	body := itervoxBranchPrefix + branchName + " -->"
	_, err := c.CreateComment(ctx, issueID, body)
	return err
}
