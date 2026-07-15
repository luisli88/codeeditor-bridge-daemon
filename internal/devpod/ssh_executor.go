package devpod

import (
	"context"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpodexec"
)

// SSHExecutor runs a command inside a devpod Workspace via `devpod ssh
// <WorkspaceID> --command <...>`, satisfying bootstrap.Executor — the
// counterpart to SubprocessRunner.Up for the steps that run *after* the
// Workspace container exists (lsp-bootstrap, claude-bootstrap). Without
// this, bootstrap.SubprocessExecutor ran `npm`/`pip`/etc. directly on the
// Bridge Daemon's own host/container, which not only doesn't have those
// toolchains installed but wouldn't install into the right container even
// if it did — see bootstrap.Executor's doc comment, which already
// flagged this as the intended production wiring.
type SSHExecutor struct {
	WorkspaceID string
}

func (e SSHExecutor) Run(ctx context.Context, name string, args ...string) error {
	// `--command` takes a single string that devpod hands to a shell
	// inside the Workspace (confirmed by hand: `--command "a && b"` runs
	// both) — name/args have to be rejoined into one shell-safe string,
	// the reverse of how exec.Command normally wants them split.
	command := devpodexec.ShellJoin(append([]string{name}, args...))
	return runDevpodCommand(ctx, nil, "ssh", e.WorkspaceID, "--command", command, "--log-output", "json")
}
