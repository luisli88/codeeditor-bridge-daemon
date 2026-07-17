package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
)

type recordingReporter struct {
	events []gitmanager.CloneProgress
}

func (r *recordingReporter) Report(progress gitmanager.CloneProgress) {
	r.events = append(r.events, progress)
}

// FR-025: the real Clone/EFS layout — a public local repo stands in for
// the real provider, exercising the actual `git clone` subprocess and
// progress parsing rather than a fake.
func TestCloner_Clone_PublicRepo_ClonesAndReportsDone(t *testing.T) {
	source := t.TempDir()
	runGit(t, source, "init", "-q", "-b", "main")
	runGit(t, source, "config", "user.email", "dev@example.com")
	runGit(t, source, "config", "user.name", "Dev")
	writeFile(t, source, "README.md", "hello\n")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-q", "-m", "initial")

	baseDir := t.TempDir()
	cloner := gitmanager.NewCloner(baseDir)
	reporter := &recordingReporter{}

	err := cloner.Clone(context.Background(), "ws-1", source, nil, reporter)

	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(cloner.WorkspacePath("ws-1"), "README.md"))
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(content))

	var sawDone bool
	for _, e := range reporter.events {
		if e.Phase == "clone" && e.Event == "done" {
			sawDone = true
		}
	}
	require.True(t, sawDone, "expected a clone/done progress event")
}

func TestCloner_Clone_InvalidSource_ReportsErrorEvent(t *testing.T) {
	baseDir := t.TempDir()
	cloner := gitmanager.NewCloner(baseDir)
	reporter := &recordingReporter{}

	err := cloner.Clone(context.Background(), "ws-1", filepath.Join(baseDir, "does-not-exist"), nil, reporter)

	require.Error(t, err)
	// A bare "exit status 128" tells a Desarrollador nothing — git's own
	// stderr ("repository ... does not exist") is what actually explains
	// the failure, and must survive into the wrapped error.
	require.NotContains(t, err.Error(), "exit status 128")
	require.Contains(t, err.Error(), "does not exist")
	var sawError bool
	for _, e := range reporter.events {
		if e.Phase == "clone" && e.Event == "error" {
			sawError = true
		}
	}
	require.True(t, sawError, "expected a clone/error progress event")
}

// Reproduces GitHub's real SSH permission-denied output, which spreads the
// actually useful diagnostic across several stderr lines with generic
// boilerplate ("and the repository exists.") as the very last one — a fake
// `git` on PATH stands in for the real subprocess so the test doesn't
// depend on network access or a real SSH key.
func TestCloner_Clone_PermissionDenied_SurfacesRealDiagnosticNotJustLastLine(t *testing.T) {
	fakeGitDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo 'git@github.com: Permission denied (publickey).' >&2\n" +
		"echo 'fatal: Could not read from remote repository.' >&2\n" +
		"echo >&2\n" +
		"echo 'Please make sure you have the correct access rights' >&2\n" +
		"echo 'and the repository exists.' >&2\n" +
		"exit 128\n"
	writeFile(t, fakeGitDir, "git", script)
	require.NoError(t, os.Chmod(filepath.Join(fakeGitDir, "git"), 0o755))
	t.Setenv("PATH", fakeGitDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	baseDir := t.TempDir()
	cloner := gitmanager.NewCloner(baseDir)
	reporter := &recordingReporter{}

	err := cloner.Clone(context.Background(), "ws-1", "git@github.com:luisli88/iegsb-portfolio.git", nil, reporter)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "exit status 128")
	require.Contains(t, err.Error(), "Permission denied (publickey)")
	require.Contains(t, err.Error(), "Could not read from remote repository")
}

func TestCloner_WorkspacePath_IsRootedAtBaseDir(t *testing.T) {
	cloner := gitmanager.NewCloner("/efs")
	require.Equal(t, "/efs/ws-1", cloner.WorkspacePath("ws-1"))
}
