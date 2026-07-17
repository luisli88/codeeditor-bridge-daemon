package gitmanager

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GitFileStatus mirrors one entry of the `git` channel's `status` phase
// payload (contracts/websocket-protocol.md — the exact shape of daily
// operations isn't pinned down there beyond "refleja 1:1 el estado real
// de git", so this is the smallest faithful reflection of `git status`).
type GitFileStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "untracked" | "modified" | "staged" | "deleted"
}

// Branch mirrors one entry of the `git` channel's `branch` phase payload.
type Branch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// Operations implements the day-to-day git operations FR-032/FR-033
// require inside a Project's container: status/diff/stage/commit/push/
// pull/branch — every one of them shells out to the real `git` binary
// against the Workspace's clone, never reimplementing git itself.
type Operations struct {
	workspacePath func(workspaceID string) string
}

// NewOperations builds an Operations rooted at workspacePath (the same
// EFS layout Cloner.WorkspacePath uses).
func NewOperations(workspacePath func(workspaceID string) string) *Operations {
	return &Operations{workspacePath: workspacePath}
}

func (o *Operations) run(ctx context.Context, workspaceID string, env []string, args ...string) (string, error) {
	workspacePath := o.workspacePath(workspaceID)
	// `devpod up` (the step right after Clone) chowns the same host-side
	// clone directory to the devcontainer's internal user so that
	// container can write to it — confirmed live: every Workspace that
	// finished provisioning ends up owned by a different UID than the
	// Bridge Daemon's own process. Git 2.35.2+ refuses to operate on a
	// repository owned by another user ("detected dubious ownership")
	// unless the directory is explicitly allowlisted — without this,
	// every git-channel operation (status/diff/branch/stage/commit/push/
	// pull) failed for any Project past that point, which is why the app
	// never showed a branch name at all rather than showing the wrong
	// one. `-c safe.directory=` scopes the exception to this one
	// invocation instead of mutating git's global config.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=" + workspacePath}, args...)...)
	cmd.Dir = workspacePath
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gitmanager: git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// Status reflects `git status --porcelain=v1` (FR-032).
func (o *Operations) Status(ctx context.Context, workspaceID string) ([]GitFileStatus, error) {
	out, err := o.run(ctx, workspaceID, nil, "status", "--porcelain=v1")
	if err != nil {
		return nil, err
	}
	var files []GitFileStatus
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		files = append(files, parseStatusLine(line))
	}
	return files, nil
}

func parseStatusLine(line string) GitFileStatus {
	x, y := line[0], line[1]
	path := strings.TrimSpace(line[3:])
	switch {
	case x == '?' && y == '?':
		return GitFileStatus{Path: path, Status: "untracked"}
	case x == 'D' || y == 'D':
		return GitFileStatus{Path: path, Status: "deleted"}
	case x != ' ':
		return GitFileStatus{Path: path, Status: "staged"}
	default:
		return GitFileStatus{Path: path, Status: "modified"}
	}
}

// Diff reflects `git diff` (worktree) or `git diff --cached` (staged),
// optionally scoped to one path (FR-032).
func (o *Operations) Diff(ctx context.Context, workspaceID string, staged bool, path string) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	if path != "" {
		args = append(args, "--", path)
	}
	return o.run(ctx, workspaceID, nil, args...)
}

// Stage runs `git add` on paths (FR-033).
func (o *Operations) Stage(ctx context.Context, workspaceID string, paths []string) error {
	_, err := o.run(ctx, workspaceID, nil, append([]string{"add"}, paths...)...)
	return err
}

// Commit runs `git commit -m message` (FR-033) and returns the new commit hash.
func (o *Operations) Commit(ctx context.Context, workspaceID, message string) (string, error) {
	if _, err := o.run(ctx, workspaceID, nil, "commit", "-m", message); err != nil {
		return "", err
	}
	hash, err := o.run(ctx, workspaceID, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(hash), err
}

// Push runs `git push`, authenticated with the Project's registered git
// credential (FR-033: "usando la credencial de git registrada del
// proyecto").
func (o *Operations) Push(ctx context.Context, workspaceID string, credential *CloneCredential) error {
	env, cleanup, err := credentialEnv(credential)
	if err != nil {
		return fmt.Errorf("gitmanager: prepare push credential: %w", err)
	}
	defer cleanup()
	_, err = o.run(ctx, workspaceID, env, "push")
	return err
}

// Pull runs `git pull`, authenticated the same way as Push. A merge
// conflict surfaces as a non-nil error here — callers check
// ConflictedFiles (merge.go) to resolve it rather than treating this as
// a hard failure.
func (o *Operations) Pull(ctx context.Context, workspaceID string, credential *CloneCredential) error {
	env, cleanup, err := credentialEnv(credential)
	if err != nil {
		return fmt.Errorf("gitmanager: prepare pull credential: %w", err)
	}
	defer cleanup()
	_, err = o.run(ctx, workspaceID, env, "pull")
	return err
}

// Branches lists every local branch, per `git branch --list`.
func (o *Operations) Branches(ctx context.Context, workspaceID string) ([]Branch, error) {
	out, err := o.run(ctx, workspaceID, nil, "branch", "--list")
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		current := strings.HasPrefix(line, "*")
		name := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		branches = append(branches, Branch{Name: name, Current: current})
	}
	return branches, nil
}

// CreateBranch runs `git checkout -b name`.
func (o *Operations) CreateBranch(ctx context.Context, workspaceID, name string) error {
	_, err := o.run(ctx, workspaceID, nil, "checkout", "-b", name)
	return err
}

// SwitchBranch runs `git checkout name`.
func (o *Operations) SwitchBranch(ctx context.Context, workspaceID, name string) error {
	_, err := o.run(ctx, workspaceID, nil, "checkout", name)
	return err
}
