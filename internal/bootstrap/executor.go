// Package bootstrap installs the Language Server(s) and the Claude Code
// CLI inside a Project's container as part of provisioning (FR-027), and
// on demand for a language not detected up front (FR-036).
package bootstrap

import (
	"context"
	"os/exec"
)

// Executor runs a command to completion — a thin seam so LSPBootstrapper
// and InstallClaudeCode are unit-testable without actually installing
// anything. SubprocessExecutor is the production implementation; in
// production it's expected to run inside the project's container (e.g.
// via `devpod ssh`), not on the Bridge Daemon's own host — that wiring
// lives with whatever invokes Provisioner (see internal/devpod).
type Executor interface {
	Run(ctx context.Context, name string, args ...string) error
}

// SubprocessExecutor runs commands directly via os/exec.
type SubprocessExecutor struct{}

func (SubprocessExecutor) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
