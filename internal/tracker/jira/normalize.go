package jira

import (
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// jiraTimeLayout matches Jira Cloud's timestamp format, e.g.
// "2024-06-01T10:15:00.000+0000" — not valid RFC3339 (no colon in the offset).
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

func parseJiraTime(v any) *time.Time {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	if t, err := time.Parse(jiraTimeLayout, s); err == nil {
		return &t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	return nil
}

// adfDoc wraps plain text in a minimal single-paragraph Atlassian Document
// Format document — the shape Jira Cloud's REST API v3 requires for
// description/comment bodies. No markdown rendering is attempted; formatting
// characters (e.g. "**", "[]()") are preserved as literal text.
func adfDoc(text string) map[string]any {
	content := []any{}
	if text != "" {
		content = append(content, map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		})
	}
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}

// blockNodeTypes are ADF node types that introduce a paragraph break when
// flattened to plain text.
var blockNodeTypes = map[string]bool{
	"paragraph":   true,
	"heading":     true,
	"blockquote":  true,
	"codeBlock":   true,
	"bulletList":  true,
	"orderedList": true,
	"listItem":    true,
}

// adfToText flattens an Atlassian Document Format value to plain text,
// concatenating "text" nodes and inserting blank lines at block boundaries.
// Returns "" for nil, malformed, or empty documents.
func adfToText(v any) string {
	doc, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	writeADFNode(&sb, doc)
	return strings.TrimSpace(sb.String())
}

func writeADFNode(sb *strings.Builder, node map[string]any) {
	nodeType, _ := node["type"].(string)
	switch nodeType {
	case "text":
		if text, ok := node["text"].(string); ok {
			sb.WriteString(text)
		}
		return
	case "hardBreak":
		sb.WriteString("\n")
		return
	}
	content, _ := node["content"].([]any)
	for _, raw := range content {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		writeADFNode(sb, child)
	}
	if blockNodeTypes[nodeType] {
		sb.WriteString("\n\n")
	}
}

func statusName(fields map[string]any) string {
	status, _ := fields["status"].(map[string]any)
	name, _ := status["name"].(string)
	return name
}

// extractComments returns the issue's comments in ascending CreatedAt order,
// matching Jira's default comment ordering and the domain.Issue.Comments contract.
func extractComments(fields map[string]any) []domain.Comment {
	commentObj, _ := fields["comment"].(map[string]any)
	rawComments, _ := commentObj["comments"].([]any)
	result := make([]domain.Comment, 0, len(rawComments))
	for _, raw := range rawComments {
		cm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := cm["id"].(string)
		author, _ := cm["author"].(map[string]any)
		authorID, _ := author["accountId"].(string)
		authorName, _ := author["displayName"].(string)
		result = append(result, domain.Comment{
			ID:         id,
			Body:       adfToText(cm["body"]),
			AuthorID:   authorID,
			AuthorName: authorName,
			CreatedAt:  parseJiraTime(cm["created"]),
		})
	}
	return result
}

// extractBlockers returns BlockerRef entries for issuelinks whose inward
// phrase is "is blocked by" — the only Jira link direction that represents a
// dependency itervox's dependency audit needs to track. State is left nil;
// callers backfill it via populateBlockerStates.
func extractBlockers(fields map[string]any) []domain.BlockerRef {
	rawLinks, _ := fields["issuelinks"].([]any)
	var result []domain.BlockerRef
	for _, raw := range rawLinks {
		link, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		linkType, _ := link["type"].(map[string]any)
		inward, _ := linkType["inward"].(string)
		if !strings.EqualFold(inward, "is blocked by") {
			continue
		}
		inwardIssue, ok := link["inwardIssue"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := inwardIssue["id"].(string)
		key, _ := inwardIssue["key"].(string)
		if id == "" && key == "" {
			continue
		}
		ref := domain.BlockerRef{}
		if id != "" {
			ref.ID = &id
		}
		if key != "" {
			ref.Identifier = &key
		}
		result = append(result, ref)
	}
	return result
}

// branchMarkerPrefix identifies a SetIssueBranch marker comment. Jira's ADF
// format has no hidden-comment mechanism (unlike GitHub's HTML comments), so
// the marker is a plain, recognizable comment body rather than truly hidden.
const branchMarkerPrefix = "itervox:branch:"

// scanBranchMarker returns the branch name from the most recent marker
// comment (last one wins), or "" if none is present.
func scanBranchMarker(comments []domain.Comment) string {
	branch := ""
	for _, c := range comments {
		if trimmed, ok := strings.CutPrefix(strings.TrimSpace(c.Body), branchMarkerPrefix); ok {
			branch = strings.TrimSpace(trimmed)
		}
	}
	return branch
}

// normalizeIssue converts a raw Jira REST API v3 issue resource to a domain.Issue.
// Returns nil if required fields (id, key) are missing.
func normalizeIssue(raw map[string]any) *domain.Issue {
	id, _ := raw["id"].(string)
	key, _ := raw["key"].(string)
	if id == "" || key == "" {
		return nil
	}
	fields, _ := raw["fields"].(map[string]any)
	if fields == nil {
		fields = map[string]any{}
	}
	summary, _ := fields["summary"].(string)

	comments := extractComments(fields)
	issue := &domain.Issue{
		ID:         id,
		Identifier: key,
		Title:      summary,
		State:      statusName(fields),
		BlockedBy:  extractBlockers(fields),
		Comments:   comments,
		CreatedAt:  parseJiraTime(fields["created"]),
		UpdatedAt:  parseJiraTime(fields["updated"]),
	}
	if desc := adfToText(fields["description"]); desc != "" {
		issue.Description = &desc
	}
	if branch := scanBranchMarker(comments); branch != "" {
		issue.BranchName = &branch
	}
	return issue
}
