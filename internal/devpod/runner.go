// Package devpod orchestrates provisioning a Project's Workspace: the
// Entitlements Gate check, clone, devcontainer.json detection, the
// `devpod up` subprocess, and the Language Server/Claude Code bootstrap
// steps, as one retryable-per-step sequence (FR-027, FR-028).
package devpod

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts invoking the `devpod` CLI (research.md §3: the Bridge
// Daemon shells out to the real binary, DevPod has no stable Go SDK) so
// Provisioner is testable without it installed. SubprocessRunner is the
// production implementation.
type Runner interface {
	Up(ctx context.Context, workspacePath string, onEvent func(line string)) error
	Delete(ctx context.Context, workspaceID string) error
}

// devpodLogLine is one `--log-output json` line — devpod's own schema
// isn't pinned down anywhere in this spec, so only the two fields this
// package actually needs are decoded; everything else is ignored.
type devpodLogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// SubprocessRunner invokes `devpod up <workspacePath> --log-output json`
// and forwards each stdout line to onEvent.
type SubprocessRunner struct{}

func (SubprocessRunner) Up(ctx context.Context, workspacePath string, onEvent func(line string)) error {
	if err := ensureDockerProvider(ctx); err != nil {
		return fmt.Errorf("devpod: ensure docker provider: %w", err)
	}

	// `--ide none`: `--open-ide` defaults to true and `--ide` defaults to
	// "vscode locally or in browser" when unset — neither makes sense for
	// a headless provisioning call with no human at a terminal to open
	// anything for. `--log-output json` (not `--output json`, which
	// doesn't exist — confirmed against a real `devpod up --help`; the
	// flag this package used before was silently rejected with "unknown
	// flag: --output", failing every single Up call) is the global flag
	// that actually produces the structured `{"level","message"}` lines
	// this function and onEvent's caller (ProvisioningStepRow's console)
	// both depend on.
	if err := runDevpodCommand(ctx, onEvent, "up", workspacePath, "--log-output", "json", "--ide", "none"); err != nil {
		return fmt.Errorf("devpod up: %w", err)
	}
	return nil
}

// Delete tears down workspaceID's devpod-managed container/infrastructure
// (Deprovision's counterpart to Up) — called when the Desarrollador
// deletes a Project. `--ignore-not-found` makes this idempotent: a
// container that's already gone (crashed, manually removed, a previous
// deprovision attempt that partly succeeded) is treated as a successful
// delete rather than an error, since the end state either way is "nothing
// left to clean up".
func (SubprocessRunner) Delete(ctx context.Context, workspaceID string) error {
	if err := runDevpodCommand(ctx, nil, "delete", workspaceID, "--force", "--ignore-not-found"); err != nil {
		return fmt.Errorf("devpod delete: %w", err)
	}
	return nil
}

// runDevpodCommand runs `devpod <args...>`, forwarding each stdout line
// to onEvent (nil if nobody needs to observe progress — SSHExecutor.Run
// doesn't), and returns devpod's own explanation of a failure — its
// `{"level":"fatal"}` line if there is one, else stderr — rather than a
// bare "exit status N". Shared between Up and SSHExecutor.Run: both shell
// out to `devpod` the same way, only the subcommand/args differ.
func runDevpodCommand(ctx context.Context, onEvent func(line string), args ...string) error {
	cmd := exec.CommandContext(ctx, "devpod", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	// stdout alone used to be the only thing captured — a failure whose
	// explanation went to stderr (or wasn't one of devpod's own
	// `{"level":"fatal"}` lines) surfaced as a bare "exit status 1", not
	// what actually went wrong.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	var lastFatal string
	scanner := bufio.NewScanner(stdout)
	// A docker image pull's progress line (or similar) can exceed
	// bufio.Scanner's 64KB default token size — grow instead of the scan
	// silently stopping with ErrTooLong.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if onEvent != nil {
			onEvent(line)
		}
		var parsed devpodLogLine
		if json.Unmarshal([]byte(line), &parsed) == nil && (parsed.Level == "fatal" || parsed.Level == "error") {
			lastFatal = parsed.Message
		}
	}

	if err := cmd.Wait(); err != nil {
		if lastFatal != "" {
			return errors.New(lastFatal)
		}
		if stderrText := strings.TrimSpace(stderr.String()); stderrText != "" {
			return fmt.Errorf("%w: %s", err, stderrText)
		}
		return err
	}
	return nil
}

// ensureDockerProvider makes sure devpod actually has a usable provider
// configured before Up runs — a fresh `devpod` install has none by
// default ("no default provider found. Please make sure to run 'devpod
// provider use'"), and neither the guided self-hosted setup
// (RemoteSetupService.swift) nor this daemon's own Docker image ever
// configured one, so every single Up call failed immediately. Idempotent:
// skips add/use entirely once "docker" already shows up in the list.
func ensureDockerProvider(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "devpod", "provider", "list").CombinedOutput()
	if err != nil {
		return fmt.Errorf("list providers: %w: %s", err, string(out))
	}
	if strings.Contains(string(out), "docker") {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "devpod", "provider", "add", "docker").CombinedOutput(); err != nil {
		return fmt.Errorf("add docker provider: %w: %s", err, string(out))
	}
	if out, err := exec.CommandContext(ctx, "devpod", "provider", "use", "docker").CombinedOutput(); err != nil {
		return fmt.Errorf("use docker provider: %w: %s", err, string(out))
	}
	return nil
}
