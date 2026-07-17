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

func TestStatus_DeletedAndStagedFiles(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "deleted.txt", "v1\n")
	writeFile(t, dir, "staged.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	require.NoError(t, os.Remove(filepath.Join(dir, "deleted.txt")))
	writeFile(t, dir, "staged.txt", "v2\n")
	runGit(t, dir, "add", "staged.txt")

	files, err := ops.Status(context.Background(), "ws-1")

	require.NoError(t, err)
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Status
	}
	require.Equal(t, "deleted", byPath["deleted.txt"])
	require.Equal(t, "staged", byPath["staged.txt"])
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

func TestDiff_StagedAndScopedToPath(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "v1\n")
	writeFile(t, dir, "b.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	writeFile(t, dir, "a.txt", "v2\n")
	writeFile(t, dir, "b.txt", "v2\n")
	runGit(t, dir, "add", "a.txt")

	staged, err := ops.Diff(context.Background(), "ws-1", true, "")
	require.NoError(t, err)
	require.Contains(t, staged, "a.txt")
	require.NotContains(t, staged, "b.txt")

	scoped, err := ops.Diff(context.Background(), "ws-1", false, "b.txt")
	require.NoError(t, err)
	require.Contains(t, scoped, "b.txt")
	require.NotContains(t, scoped, "a.txt")
}

// `devpod up` chowns the host-side clone to the devcontainer's internal
// user, so every git-channel operation past that point runs against a
// directory git 2.35.2+ would otherwise refuse to touch ("detected
// dubious ownership") — a real chown in this test would need root, so
// this stands in a fake `git` on PATH and asserts the actual argv instead
// of reproducing the ownership mismatch itself.
func TestOperations_EveryGitInvocation_ScopesSafeDirectoryToTheWorkspacePath(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls")
	fakeGitDir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" >> " + callsPath + "\necho ''\n"
	writeFile(t, fakeGitDir, "git", script)
	require.NoError(t, os.Chmod(filepath.Join(fakeGitDir, "git"), 0o755))
	t.Setenv("PATH", fakeGitDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workspaceDir := t.TempDir()
	ops := gitmanager.NewOperations(func(workspaceID string) string { return workspaceDir })
	_, _ = ops.Branches(context.Background(), "ws-1")

	calls, err := os.ReadFile(callsPath)
	require.NoError(t, err)
	require.Contains(t, string(calls), "-c safe.directory="+workspaceDir)
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

// FR-033: Push publishes local commits to the registered remote — a
// local bare repo stands in for the real git provider so this exercises
// real `git push`, not a fake.
func TestPush_ToRemote_PublishesCommit(t *testing.T) {
	central := t.TempDir()
	runGit(t, central, "init", "-q", "--bare", "-b", "main")

	// Cloning (rather than `git init` + manually adding a remote) is what
	// sets up the upstream tracking branch `git push` needs — the same
	// setup Cloner.Clone leaves a real Workspace in.
	dir := t.TempDir()
	runGit(t, dir, "clone", "-q", central, ".")
	runGit(t, dir, "config", "user.email", "dev@example.com")
	runGit(t, dir, "config", "user.name", "Dev")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })

	writeFile(t, dir, "file.txt", "content\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	err := ops.Push(context.Background(), "ws-1", nil)

	require.NoError(t, err)
	logOut := runGit(t, central, "log", "--oneline")
	require.Contains(t, logOut, "initial")
}

func TestPull_FromRemote_FetchesNewCommit(t *testing.T) {
	central := t.TempDir()
	runGit(t, central, "init", "-q", "--bare", "-b", "main")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", "-q", central, ".")
	runGit(t, seedDir, "config", "user.email", "dev@example.com")
	runGit(t, seedDir, "config", "user.name", "Dev")
	writeFile(t, seedDir, "shared.txt", "v1\n")
	runGit(t, seedDir, "add", ".")
	runGit(t, seedDir, "commit", "-q", "-m", "shared v1")
	runGit(t, seedDir, "push", "-q", "origin", "main")

	dir := t.TempDir()
	runGit(t, dir, "clone", "-q", central, ".")
	ops := gitmanager.NewOperations(func(workspaceID string) string { return dir })

	otherDir := t.TempDir()
	runGit(t, otherDir, "clone", "-q", central, ".")
	runGit(t, otherDir, "config", "user.email", "dev@example.com")
	runGit(t, otherDir, "config", "user.name", "Dev")
	writeFile(t, otherDir, "shared.txt", "v2\n")
	runGit(t, otherDir, "commit", "-q", "-am", "shared v2")
	runGit(t, otherDir, "push", "-q", "origin", "main")

	err := ops.Pull(context.Background(), "ws-1", nil)

	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
	require.NoError(t, err)
	require.Equal(t, "v2\n", string(content))
}

func TestPush_UnsupportedCredentialKind_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	err := ops.Push(context.Background(), "ws-1", &gitmanager.CloneCredential{Kind: gitmanager.CredentialKind("unknown")})

	require.Error(t, err)
}

func TestPull_UnsupportedCredentialKind_ReturnsError(t *testing.T) {
	ops, dir := newTestRepo(t)
	writeFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")

	err := ops.Pull(context.Background(), "ws-1", &gitmanager.CloneCredential{Kind: gitmanager.CredentialKind("unknown")})

	require.Error(t, err)
}
