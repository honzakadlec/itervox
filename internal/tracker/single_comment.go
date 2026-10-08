package tracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// InputRequiredCommentPrefix starts every input-required question comment.
// SingleCommentTracker never consolidates these: reply detection anchors on
// the question comment's position, so it must stay a standalone comment.
const InputRequiredCommentPrefix = "🤖 **Agent needs your input**"

// SingleCommentMarker tags the consolidated per-issue Itervox comment so it
// can be rediscovered after a daemon restart.
const SingleCommentMarker = "<!-- itervox:single-comment -->"

const (
	singleCommentHeader    = "🤖 Itervox agent updates"
	singleCommentSeparator = "\n\n---\n\n"
	// singleCommentMaxLen keeps the consolidated body under Jira's 32767
	// character comment limit; the oldest entries are dropped first.
	singleCommentMaxLen = 30000
)

// BranchMarkerFormatter is an optional interface for adapters whose
// SetIssueBranch marker is a plain comment line (Jira). SingleCommentTracker
// folds the marker into the consolidated comment instead of posting it
// separately; the adapter must recognise the line anywhere in a comment body.
type BranchMarkerFormatter interface {
	BranchMarkerLine(branchName string) string
}

type singleCommentUpdater interface {
	Tracker
	CommentUpdater
}

type singleCommentRef struct {
	id      string
	entries []string
}

// SingleCommentTracker wraps a Tracker so every managed comment (see
// MarkManagedComment) is appended to one Itervox comment per issue, edited in
// place, instead of posting a new comment each time. Unmanaged comments and
// input-required questions pass through unchanged.
//
// The wrapper exposes only the Tracker and CommentUpdater methods, so optional
// interfaces of the inner adapter (RateLimiter, ProjectManager) are hidden.
type SingleCommentTracker struct {
	singleCommentUpdater
	now func() time.Time

	mu   sync.Mutex
	refs map[string]*singleCommentRef // issueID → consolidated comment
}

// NewSingleCommentTracker returns inner wrapped in a SingleCommentTracker, or
// inner unchanged when the adapter cannot edit comments.
func NewSingleCommentTracker(inner Tracker) Tracker {
	updater, ok := inner.(singleCommentUpdater)
	if !ok {
		slog.Warn("tracker: single_comment is not supported by this tracker adapter; posting separate comments")
		return inner
	}
	return &SingleCommentTracker{
		singleCommentUpdater: updater,
		now:                  time.Now,
		refs:                 make(map[string]*singleCommentRef),
	}
}

// CreateComment appends managed comments to the issue's consolidated comment.
func (s *SingleCommentTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	trimmed := strings.TrimSpace(body)
	if !strings.Contains(trimmed, ManagedCommentMarker) || strings.HasPrefix(trimmed, InputRequiredCommentPrefix) {
		return s.singleCommentUpdater.CreateComment(ctx, issueID, body)
	}
	entry := strings.TrimSpace(strings.ReplaceAll(trimmed, ManagedCommentMarker, ""))
	return s.appendEntry(ctx, issueID, entry)
}

// SetIssueBranch folds the branch marker into the consolidated comment when
// the adapter supports it; otherwise it delegates to the inner adapter.
func (s *SingleCommentTracker) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	formatter, ok := s.singleCommentUpdater.(BranchMarkerFormatter)
	if !ok {
		return s.singleCommentUpdater.SetIssueBranch(ctx, issueID, branchName)
	}
	if _, err := s.appendEntry(ctx, issueID, formatter.BranchMarkerLine(branchName)); err != nil {
		return fmt.Errorf("tracker: set issue branch on %s: %w", issueID, err)
	}
	return nil
}

func (s *SingleCommentTracker) appendEntry(ctx context.Context, issueID, entry string) (*domain.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := s.lookup(ctx, issueID)
	if err != nil {
		return nil, err
	}
	stamped := "**" + s.now().UTC().Format("2006-01-02 15:04 UTC") + "**\n" + entry

	if ref != nil {
		entries := append(append([]string(nil), ref.entries...), stamped)
		body := renderSingleComment(entries)
		comment, err := s.UpdateComment(ctx, issueID, ref.id, body)
		if err == nil {
			ref.entries = entries
			return comment, nil
		}
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			return nil, err
		}
		// The consolidated comment was deleted on the tracker; start a new one.
		delete(s.refs, issueID)
	}

	entries := []string{stamped}
	comment, err := s.singleCommentUpdater.CreateComment(ctx, issueID, renderSingleComment(entries))
	if err != nil {
		return nil, err
	}
	if comment != nil && comment.ID != "" {
		s.refs[issueID] = &singleCommentRef{id: comment.ID, entries: entries}
	}
	return comment, nil
}

// lookup returns the cached consolidated comment for issueID, falling back to
// scanning the issue's comments (e.g. after a daemon restart). Caller holds mu.
func (s *SingleCommentTracker) lookup(ctx context.Context, issueID string) (*singleCommentRef, error) {
	if ref, ok := s.refs[issueID]; ok {
		return ref, nil
	}
	issue, err := s.FetchIssueDetail(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("tracker: find single comment on %s: %w", issueID, err)
	}
	if issue == nil {
		return nil, nil
	}
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, SingleCommentMarker) {
			ref := &singleCommentRef{id: c.ID, entries: parseSingleComment(c.Body)}
			s.refs[issueID] = ref
			return ref, nil
		}
	}
	return nil, nil
}

// renderSingleComment builds the consolidated body, dropping the oldest
// entries until it fits singleCommentMaxLen (the newest entry is always kept).
func renderSingleComment(entries []string) string {
	for {
		body := singleCommentHeader + "\n\n" + strings.Join(entries, singleCommentSeparator) +
			"\n\n" + ManagedCommentMarker + "\n" + SingleCommentMarker
		if len(body) <= singleCommentMaxLen || len(entries) <= 1 {
			return body
		}
		entries = entries[1:]
	}
}

// parseSingleComment recovers the entries from a consolidated comment body.
func parseSingleComment(body string) []string {
	body = strings.ReplaceAll(body, SingleCommentMarker, "")
	body = strings.ReplaceAll(body, ManagedCommentMarker, "")
	body = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body), singleCommentHeader))
	if body == "" {
		return nil
	}
	var entries []string
	for part := range strings.SplitSeq(body, singleCommentSeparator) {
		if part = strings.TrimSpace(part); part != "" {
			entries = append(entries, part)
		}
	}
	return entries
}
