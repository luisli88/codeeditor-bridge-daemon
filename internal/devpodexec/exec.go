// Package devpodexec starts a subprocess inside a devpod Workspace via
// `devpod ssh <workspaceID> --command ...` rather than on the Bridge
// Daemon's own host — the shared mechanism behind the LSP proxy, the
// debug adapter proxy, and Run execution, all of which need a process
// that actually runs where the Workspace's toolchain (node, python,
// gopls, ...) is installed, not wherever the daemon itself happens to be
// running.
//
// Verified by hand against a real `devpod ssh`: its own log noise
// ("Error tunneling to container: wait: remote command exited without
// exit status or exit signal", printed even on a clean exit) lands on
// stderr, never mixing into the piped stdout; stdin/stdout stream live
// rather than buffering until EOF; and its default working directory
// inside the container is already the Workspace root
// (/workspaces/<workspaceID>), matching devpod's own naming convention
// for a Workspace created from `devpod up <baseDir>/<workspaceID>`
// (gitmanager.Cloner.WorkspacePath) — so nothing here needs its own
// --workdir override. A real LSP `initialize` request round-tripped
// cleanly through the tunnel to `vtsls --stdio` this way.
package devpodexec

import (
	"context"
	"io"
	"os/exec"
	"strings"
)

// StartPiped starts name+args inside workspaceID's devpod Workspace,
// returning live stdin/stdout pipes exactly like a direct
// exec.CommandContext(ctx, name, args...) would via StdinPipe/StdoutPipe
// — the only difference is *where* it runs. Stderr is left unpiped (goes
// to the null device, same as it always has for the LSP/debug proxies
// this is for): nobody reads DAP/LSP protocol data from stderr, and
// piping it without a reader risks the child blocking once the pipe
// buffer fills.
func StartPiped(ctx context.Context, workspaceID, name string, args ...string) (cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser, err error) {
	cmd = command(ctx, workspaceID, name, args...)
	stdin, err = cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, stdin, stdout, nil
}

// StartWithOutputPipes starts name+args inside workspaceID's devpod
// Workspace, returning live stdout/stderr pipes — for Run execution,
// which streams both separately and never writes to the process's stdin.
func StartWithOutputPipes(ctx context.Context, workspaceID, name string, args ...string) (cmd *exec.Cmd, stdout, stderr io.ReadCloser, err error) {
	cmd = command(ctx, workspaceID, name, args...)
	stdout, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err = cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, stdout, stderr, nil
}

func command(ctx context.Context, workspaceID, name string, args ...string) *exec.Cmd {
	joined := ShellJoin(append([]string{name}, args...))
	return exec.CommandContext(ctx, "devpod", "ssh", workspaceID, "--command", joined)
}

// tunnelTeardownNoise is devpod ssh's own diagnostic line, printed to
// stderr on every single invocation regardless of whether the remote
// command actually failed — confirmed by hand: it shows up even after a
// clean `exit 0`. StartWithOutputPipes callers that relay stderr to a
// real developer (Run execution) need to not present this as if it were
// the running program's own output.
const tunnelTeardownNoise = "Error tunneling to container: wait: remote command exited without exit status or exit signal"

// IsOwnDiagnosticNoise reports whether line is devpod ssh's own tunnel
// teardown message rather than output from the command that actually ran
// inside the Workspace.
func IsOwnDiagnosticNoise(line string) bool {
	return strings.Contains(line, tunnelTeardownNoise)
}

// ShellJoin quotes each part for a POSIX shell and joins them with
// spaces — devpod's --command re-interprets its value through a shell
// inside the Workspace, so name/args (Go argv elements, never
// shell-interpreted on this side) need real quoting to survive being
// rejoined into that single string, not a plain strings.Join. A part
// with a shell metacharacter (a Run command's own "a && b", or `sh -c`'s
// script argument) has to come through as that literal text once the
// remote shell re-parses it.
func ShellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = "'" + strings.ReplaceAll(part, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
