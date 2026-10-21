package workspace_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/workspace"
)

// bareModeManager returns a worktree-mode manager backed by a bare clone of a
// fresh upstream, plus a working clone of that upstream to push commits from.
func bareModeManager(t *testing.T) (mgr *workspace.Manager, upstream, pusher string) {
	t.Helper()
	upstream = setupUpstreamRepo(t)
	pusher = filepath.Join(t.TempDir(), "pusher")
	git(t, ".", "clone", upstream, pusher)
	git(t, pusher, "config", "user.email", "test@test.com")
	git(t, pusher, "config", "user.name", "Test")

	cfg := newWorktreeConfig(t.TempDir())
	cfg.Workspace.CloneURL = upstream
	cfg.Workspace.BaseBranch = "main"
	return workspace.NewManager(cfg), upstream, pusher
}

// pushCommit commits name=content on branch in the pusher clone and pushes it,
// returning the new commit hash.
func pushCommit(t *testing.T, pusher, branch, name, content string) string {
	t.Helper()
	git(t, pusher, "checkout", "-B", branch)
	writeFile(t, pusher, name, content)
	git(t, pusher, "add", ".")
	git(t, pusher, "commit", "-m", "update "+name)
	git(t, pusher, "push", "--force", "origin", branch)
	return strings.TrimSpace(git(t, pusher, "rev-parse", "HEAD"))
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
}

// A retry or requeue reuses the issue's worktree. A fix merged into the base
// while the issue was failing must reach that worktree, or every retry fails
// the same way.
func TestEnsureWorktreeReuseFastForwardsToUpdatedBase(t *testing.T) {
	mgr, _, pusher := bareModeManager(t)
	ctx := context.Background()

	ws, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.True(t, ws.CreatedNow)

	// Hook output left in the tree must not stop the refresh.
	writeFile(t, ws.Path, "build-artifact.js", "generated\n")
	fixed := pushCommit(t, pusher, "main", "fix.txt", "fixed\n")

	ws2, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.False(t, ws2.CreatedNow)
	assert.Equal(t, fixed, headOf(t, ws2.Path), "reused worktree must carry the base fix")
	assert.FileExists(t, filepath.Join(ws2.Path, "build-artifact.js"))
}

// A branch with its own commits may already back a pushed PR. Moving it would
// diverge from that PR, so it is left exactly as it was.
func TestEnsureWorktreeReuseLeavesBranchWithOwnCommits(t *testing.T) {
	mgr, _, pusher := bareModeManager(t)
	ctx := context.Background()

	ws, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	writeFile(t, ws.Path, "work.txt", "agent work\n")
	git(t, ws.Path, "add", ".")
	git(t, ws.Path, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "agent work")
	before := headOf(t, ws.Path)

	pushCommit(t, pusher, "main", "fix.txt", "fixed\n")

	ws2, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	assert.Equal(t, before, headOf(t, ws2.Path))
}

// The bare clone maps remote branches straight onto refs/heads/*. Once an
// issue branch is pushed and checked out in a worktree, `git fetch --all`
// refuses to update it and aborts the whole origin fetch, leaving the base
// stale for every later dispatch. Both a fresh worktree and a reused one must
// still see the current base.
func TestEnsureWorktreeSeesBaseWhenCheckedOutBranchBlocksFetch(t *testing.T) {
	mgr, _, pusher := bareModeManager(t)
	ctx := context.Background()

	pushCommit(t, pusher, "itervox/eng-1", "pr.txt", "v1\n")
	git(t, pusher, "checkout", "main")
	_, err := mgr.EnsureWorkspace(ctx, "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	pushCommit(t, pusher, "itervox/eng-1", "pr.txt", "v2\n")

	fixed := pushCommit(t, pusher, "main", "fix.txt", "fixed\n")

	ws2, err := mgr.EnsureWorkspace(ctx, "ENG-2", "itervox/eng-2")
	require.NoError(t, err)
	require.True(t, ws2.CreatedNow)
	assert.Equal(t, fixed, headOf(t, ws2.Path), "fresh worktree must start from the current base")

	fixed2 := pushCommit(t, pusher, "main", "fix2.txt", "fixed again\n")
	ws3, err := mgr.EnsureWorkspace(ctx, "ENG-2", "itervox/eng-2")
	require.NoError(t, err)
	assert.Equal(t, fixed2, headOf(t, ws3.Path), "reused worktree must reach the current base")
}
