// Package devpod orchestrates provisioning a Project's Workspace: the
// Entitlements Gate check, clone, devcontainer.json detection, the
// `devpod up` subprocess, and the Language Server/Claude Code bootstrap
// steps, as one retryable-per-step sequence (FR-027, FR-028).
package devpod

import (
	"bufio"
	"context"
	"os/exec"
)

// Runner abstracts invoking the `devpod` CLI (research.md §3: the Bridge
// Daemon shells out to the real binary, DevPod has no stable Go SDK) so
// Provisioner is testable without it installed. SubprocessRunner is the
// production implementation — nobody has run this against a real `devpod`
// binary in this environment, so treat that specific integration as
// unverified, same as the Bridge Daemon's other external-process
// integrations (mosh SSP, the git credential SSH reachability check).
type Runner interface {
	Up(ctx context.Context, workspacePath string, onEvent func(line string)) error
}

// SubprocessRunner invokes `devpod up <workspacePath> --output json`
// (research.md §3) and forwards each stdout line to onEvent. DevPod's
// exact `--output json` schema isn't pinned down anywhere in this spec,
// so lines are forwarded raw rather than parsed into typed fields that
// would just be guesses.
type SubprocessRunner struct{}

func (SubprocessRunner) Up(ctx context.Context, workspacePath string, onEvent func(line string)) error {
	cmd := exec.CommandContext(ctx, "devpod", "up", workspacePath, "--output", "json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		onEvent(scanner.Text())
	}
	return cmd.Wait()
}
