package jira

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
)

func TestAdfToTextSimpleParagraph(t *testing.T) {
	doc := map[string]any{
		"type":    "doc",
		"version": float64(1),
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Hello world"},
				},
			},
		},
	}
	assert.Equal(t, "Hello world", adfToText(doc))
}

func TestAdfToTextMultipleParagraphs(t *testing.T) {
	doc := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "First"}}},
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Second"}}},
		},
	}
	assert.Equal(t, "First\n\nSecond", adfToText(doc))
}

func TestAdfToTextNilReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", adfToText(nil))
	assert.Equal(t, "", adfToText(map[string]any{"type": "doc", "content": []any{}}))
}

func TestAdfDocRoundtrip(t *testing.T) {
	doc := adfDoc("plain text body")
	assert.Equal(t, "plain text body", adfToText(doc))
}

func TestAdfDocEmptyText(t *testing.T) {
	doc := adfDoc("")
	assert.Equal(t, "", adfToText(doc))
}

func TestNormalizeIssueBasicFields(t *testing.T) {
	raw := map[string]any{
		"id":  "10001",
		"key": "PROJ-1",
		"fields": map[string]any{
			"summary": "Fix the bug",
			"status":  map[string]any{"name": "In Progress"},
			"description": map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Body text"}}},
				},
			},
			"created": "2024-06-01T10:15:00.000+0000",
			"updated": "2024-06-02T10:15:00.000+0000",
		},
	}
	issue := normalizeIssue(raw)
	require.NotNil(t, issue)
	assert.Equal(t, "10001", issue.ID)
	assert.Equal(t, "PROJ-1", issue.Identifier)
	assert.Equal(t, "Fix the bug", issue.Title)
	assert.Equal(t, "In Progress", issue.State)
	require.NotNil(t, issue.Description)
	assert.Equal(t, "Body text", *issue.Description)
	require.NotNil(t, issue.CreatedAt)
	require.NotNil(t, issue.UpdatedAt)
}

func TestNormalizeIssueMissingKeyReturnsNil(t *testing.T) {
	raw := map[string]any{"id": "10001", "fields": map[string]any{}}
	assert.Nil(t, normalizeIssue(raw))
}

func TestExtractBlockersOnlyInwardBlockedBy(t *testing.T) {
	fields := map[string]any{
		"issuelinks": []any{
			map[string]any{
				"type":        map[string]any{"inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": map[string]any{"id": "9", "key": "PROJ-9"},
			},
			map[string]any{
				"type":         map[string]any{"inward": "is blocked by", "outward": "blocks"},
				"outwardIssue": map[string]any{"id": "5", "key": "PROJ-5"},
			},
		},
	}
	blockers := extractBlockers(fields)
	require.Len(t, blockers, 1)
	require.NotNil(t, blockers[0].Identifier)
	assert.Equal(t, "PROJ-9", *blockers[0].Identifier)
}

func TestExtractCommentsOrderPreserved(t *testing.T) {
	fields := map[string]any{
		"comment": map[string]any{
			"comments": []any{
				map[string]any{
					"id":     "1",
					"body":   adfDoc("first"),
					"author": map[string]any{"displayName": "Bot", "accountId": "acc1"},
				},
				map[string]any{
					"id":   "2",
					"body": adfDoc("second"),
				},
			},
		},
	}
	comments := extractComments(fields)
	require.Len(t, comments, 2)
	assert.Equal(t, "first", comments[0].Body)
	assert.Equal(t, "Bot", comments[0].AuthorName)
	assert.Equal(t, "second", comments[1].Body)
}

func TestScanBranchMarkerLastWins(t *testing.T) {
	comments := []domain.Comment{
		{Body: "itervox:branch:feature-old"},
		{Body: "some unrelated comment"},
		{Body: "itervox:branch:feature-new"},
	}
	assert.Equal(t, "feature-new", scanBranchMarker(comments))
}

func TestScanBranchMarkerNoneReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", scanBranchMarker([]domain.Comment{{Body: "hello"}}))
}
