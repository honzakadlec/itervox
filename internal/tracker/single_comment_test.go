package tracker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// branchMemoryTracker adds the Jira-style BranchMarkerFormatter to MemoryTracker.
type branchMemoryTracker struct{ *tracker.MemoryTracker }

func (branchMemoryTracker) BranchMarkerLine(branchName string) string {
	return "itervox:branch:" + branchName
}

func singleCommentFixture() *tracker.MemoryTracker {
	return tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "Todo")}, []string{"Todo"}, nil)
}

func issueComments(t *testing.T, tr tracker.Tracker) []domain.Comment {
	t.Helper()
	issue, err := tr.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, err)
	return issue.Comments
}

func TestSingleCommentTrackerAppendsManagedCommentsToOneComment(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewSingleCommentTracker(singleCommentFixture())

	first, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("Opened MR: https://example/1"))
	require.NoError(t, err)
	second, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("AI review passed"))
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID)
	comments := issueComments(t, tr)
	require.Len(t, comments, 1)
	body := comments[0].Body
	assert.Contains(t, body, "Opened MR: https://example/1")
	assert.Contains(t, body, "AI review passed")
	assert.Less(t, strings.Index(body, "Opened MR"), strings.Index(body, "AI review passed"))
	assert.Equal(t, 1, strings.Count(body, tracker.ManagedCommentMarker))
	assert.True(t, tracker.IsManagedComment(comments[0]))
}

func TestSingleCommentTrackerPassesThroughUnmanagedAndInputRequired(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewSingleCommentTracker(singleCommentFixture())

	_, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("status"))
	require.NoError(t, err)
	_, err = tr.CreateComment(ctx, "id1", "human reply relayed from dashboard")
	require.NoError(t, err)
	_, err = tr.CreateComment(ctx, "id1", tracker.MarkManagedComment(tracker.InputRequiredCommentPrefix+"\n\nWhich option?"))
	require.NoError(t, err)

	assert.Len(t, issueComments(t, tr), 3)
}

func TestSingleCommentTrackerRediscoversCommentAfterRestart(t *testing.T) {
	ctx := context.Background()
	inner := singleCommentFixture()
	_, err := tracker.NewSingleCommentTracker(inner).CreateComment(ctx, "id1", tracker.MarkManagedComment("before restart"))
	require.NoError(t, err)

	restarted := tracker.NewSingleCommentTracker(inner)
	_, err = restarted.CreateComment(ctx, "id1", tracker.MarkManagedComment("after restart"))
	require.NoError(t, err)

	comments := issueComments(t, inner)
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "before restart")
	assert.Contains(t, comments[0].Body, "after restart")
}

// deletedCommentTracker reports every comment edit as NotFound, as Jira does
// after someone deletes the consolidated comment.
type deletedCommentTracker struct{ *tracker.MemoryTracker }

func (deletedCommentTracker) UpdateComment(context.Context, string, string, string) (*domain.Comment, error) {
	return nil, &tracker.NotFoundError{Adapter: "memory"}
}

func TestSingleCommentTrackerRecreatesDeletedComment(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewSingleCommentTracker(deletedCommentTracker{singleCommentFixture()})
	first, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("one"))
	require.NoError(t, err)

	second, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("two"))
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID)
	comments := issueComments(t, tr)
	require.Len(t, comments, 2)
	assert.Contains(t, comments[1].Body, "two")
	assert.NotContains(t, comments[1].Body, "one")
}

func TestSingleCommentTrackerFoldsBranchMarker(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewSingleCommentTracker(branchMemoryTracker{singleCommentFixture()})

	require.NoError(t, tr.SetIssueBranch(ctx, "id1", "eng-1"))
	_, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment("done"))
	require.NoError(t, err)

	comments := issueComments(t, tr)
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "\nitervox:branch:eng-1")
}

func TestSingleCommentTrackerTrimsOldestEntries(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewSingleCommentTracker(singleCommentFixture())
	chunk := strings.Repeat("x", 9000)
	for i := range 5 {
		_, err := tr.CreateComment(ctx, "id1", tracker.MarkManagedComment(string(rune('A'+i))+chunk))
		require.NoError(t, err)
	}

	body := issueComments(t, tr)[0].Body
	assert.LessOrEqual(t, len(body), 30000)
	assert.NotContains(t, body, "A"+chunk)
	assert.Contains(t, body, "E"+chunk)
}
