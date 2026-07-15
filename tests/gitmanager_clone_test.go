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

func TestCloner_WorkspacePath_IsRootedAtBaseDir(t *testing.T) {
	cloner := gitmanager.NewCloner("/efs")
	require.Equal(t, "/efs/ws-1", cloner.WorkspacePath("ws-1"))
}
