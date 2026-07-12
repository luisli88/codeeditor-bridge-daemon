package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
)

// newTestRepo creates a real git repository in a temp dir and returns an
// Operations rooted at it — every test here exercises the real `git`
// binary, not a fake, since operations.go is a thin wrapper around it.
func newTestRepo(t *testing.T) (*gitmanager.Operations, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "dev@example.com")
	runGit(t, dir, "config", "user.name", "Dev")

	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })
	return ops, dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestStatus_UntrackedAndModifiedFiles(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "tracked.txt", "v1\n")
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	writeFile(t, dir, "tracked.txt", "v2\n")
	writeFile(t, dir, "new.txt", "new\n")

	files, err := ops.Status(context.Background(), "ws-1")

	require.NoError(t, err)
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Status
	}
	require.Equal(t, "modified", byPath["tracked.txt"])
	require.Equal(t, "untracked", byPath["new.txt"])
}

func TestStageAndCommit_ReturnsCommitHash(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "content\n")
	ctx := context.Background()

	require.NoError(t, ops.Stage(ctx, "ws-1", []string{"file.txt"}))
	hash, err := ops.Commit(ctx, "ws-1", "add file")

	require.NoError(t, err)
	require.Len(t, hash, 40) // full SHA-1
}

func TestDiff_ReflectsUncommittedChange(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	writeFile(t, dir, "file.txt", "v2\n")

	diff, err := ops.Diff(context.Background(), "ws-1", false, "")

	require.NoError(t, err)
	require.Contains(t, diff, "-v1")
	require.Contains(t, diff, "+v2")
}

func TestBranches_ListsAndMarksCurrent(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	ctx := context.Background()

	require.NoError(t, ops.CreateBranch(ctx, "ws-1", "feature"))
	branches, err := ops.Branches(ctx, "ws-1")

	require.NoError(t, err)
	var current string
	names := map[string]bool{}
	for _, b := range branches {
		names[b.Name] = true
		if b.Current {
			current = b.Name
		}
	}
	require.True(t, names["main"])
	require.True(t, names["feature"])
	require.Equal(t, "feature", current) // CreateBranch checks out the new branch
}

// FR-035
func TestMergeConflict_ResolveMineTheirsAndManual_StagesCleanFile(t *testing.T) {
	ops, dir := newTestRepo(t)
	ctx := context.Background()

	writeFile(t, dir, "a.txt", "base-a\n")
	writeFile(t, dir, "b.txt", "base-b\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature-a\n")
	writeFile(t, dir, "b.txt", "feature-b\n")
	runGit(t, dir, "commit", "-q", "-am", "feature change")

	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "main-a\n")
	writeFile(t, dir, "b.txt", "main-b\n")
	runGit(t, dir, "commit", "-q", "-am", "main change")

	mergeCmd := exec.Command("git", "merge", "feature")
	mergeCmd.Dir = dir
	_ = mergeCmd.Run() // expected to fail with conflicts

	conflicted, err := ops.ConflictedFiles(ctx, "ws-1")
	require.NoError(t, err)
	require.Len(t, conflicted, 2)

	var aFile, bFile gitmanager.ConflictedFile
	for _, f := range conflicted {
		switch f.Path {
		case "a.txt":
			aFile = f
		case "b.txt":
			bFile = f
		}
	}
	require.Len(t, aFile.Blocks, 1)
	require.Len(t, bFile.Blocks, 1)

	require.NoError(t, ops.ResolveBlock(ctx, "ws-1", "a.txt", 0, gitmanager.ConflictResolutionMine, ""))
	require.NoError(t, ops.ResolveBlock(ctx, "ws-1", "b.txt", 0, gitmanager.ConflictResolutionTheirs, ""))

	aContent, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "main-a\n", string(aContent))

	bContent, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "feature-b\n", string(bContent))

	require.NoError(t, ops.StageResolvedFile(ctx, "ws-1", "a.txt"))
	require.NoError(t, ops.StageResolvedFile(ctx, "ws-1", "b.txt"))

	remaining, err := ops.ConflictedFiles(ctx, "ws-1")
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestMergeConflict_ManualResolution_WritesExactText(t *testing.T) {
	ops, dir := newTestRepo(t)
	ctx := context.Background()

	writeFile(t, dir, "a.txt", "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature\n")
	runGit(t, dir, "commit", "-q", "-am", "feature change")

	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "main\n")
	runGit(t, dir, "commit", "-q", "-am", "main change")

	mergeCmd := exec.Command("git", "merge", "feature")
	mergeCmd.Dir = dir
	_ = mergeCmd.Run()

	require.NoError(t, ops.ResolveBlock(ctx, "ws-1", "a.txt", 0, gitmanager.ConflictResolutionManual, "merged-by-hand"))

	content, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "merged-by-hand\n", string(content))
}
