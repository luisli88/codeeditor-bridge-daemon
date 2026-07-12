package gitmanager

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// CloneProgress mirrors the `git` channel's clone payload
// (contracts/websocket-protocol.md → Canal `git`).
type CloneProgress struct {
	Phase           string `json:"phase"`
	Event           string `json:"event"`
	ObjectsReceived int    `json:"objectsReceived"`
	TotalObjects    int    `json:"totalObjects"`
	DeltasResolved  int    `json:"deltasResolved"`
}

// ProgressReporter abstracts sending a CloneProgress over the WebSocket
// (`ws.Conn.SendPayload`) so Clone is testable without a real connection.
type ProgressReporter interface {
	Report(progress CloneProgress)
}

// ReporterFunc adapts a plain function to ProgressReporter.
type ReporterFunc func(progress CloneProgress)

func (f ReporterFunc) Report(progress CloneProgress) { f(progress) }

var (
	receivingObjectsRe = regexp.MustCompile(`Receiving objects:\s+\d+%\s+\((\d+)/(\d+)\)`)
	resolvingDeltasRe  = regexp.MustCompile(`Resolving deltas:\s+\d+%\s+\((\d+)/(\d+)\)`)
)

// CloneCredential is the subset of Credential Clone needs to authenticate
// — never the full record, so a caller can't accidentally leak SecretRef
// or metadata into a git subprocess's environment.
type CloneCredential struct {
	Kind   CredentialKind
	Secret string
}

// Cloner clones repositories to EFS-backed storage (FR-025: the full clone
// MUST finish before any container exists, so the repo's own
// devcontainer.json can be read).
type Cloner struct {
	baseDir string
}

// NewCloner builds a Cloner rooted at baseDir (the EFS mount point).
func NewCloner(baseDir string) *Cloner {
	return &Cloner{baseDir: baseDir}
}

// WorkspacePath is where workspaceID's repository lives once cloned.
func (c *Cloner) WorkspacePath(workspaceID string) string {
	return filepath.Join(c.baseDir, workspaceID)
}

// Clone runs `git clone --progress`, streaming parsed progress to
// reporter. The credential (if any) is passed via a short-lived
// environment/temp-file, never as a URL or argv component — both of
// those are visible to any other process on the host via `ps`.
func (c *Cloner) Clone(
	ctx context.Context,
	workspaceID, repositoryURL string,
	credential *CloneCredential,
	reporter ProgressReporter,
) error {
	dest := c.WorkspacePath(workspaceID)

	cmd := exec.CommandContext(ctx, "git", "clone", "--progress", repositoryURL, dest)
	env, cleanup, err := c.credentialEnv(credential)
	if err != nil {
		return fmt.Errorf("gitmanager: prepare clone credential: %w", err)
	}
	defer cleanup()
	cmd.Env = append(os.Environ(), env...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("gitmanager: attach clone stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gitmanager: start clone: %w", err)
	}

	scanner := bufio.NewScanner(stderr)
	// git's progress output uses \r to redraw a line in place rather than
	// \n between updates — split on \r too, or every intermediate
	// percentage gets silently coalesced into one giant "line".
	scanner.Split(scanLinesOrCarriageReturns)
	for scanner.Scan() {
		reportCloneProgressLine(scanner.Text(), reporter)
	}

	if err := cmd.Wait(); err != nil {
		reporter.Report(CloneProgress{Phase: "clone", Event: "error"})
		return fmt.Errorf("gitmanager: clone %s: %w", repositoryURL, err)
	}
	reporter.Report(CloneProgress{Phase: "clone", Event: "done"})
	return nil
}

func reportCloneProgressLine(line string, reporter ProgressReporter) {
	if m := receivingObjectsRe.FindStringSubmatch(line); m != nil {
		reporter.Report(CloneProgress{
			Phase: "clone", Event: "progress",
			ObjectsReceived: atoiSafe(m[1]), TotalObjects: atoiSafe(m[2]),
		})
		return
	}
	if m := resolvingDeltasRe.FindStringSubmatch(line); m != nil {
		reporter.Report(CloneProgress{
			Phase: "clone", Event: "progress",
			DeltasResolved: atoiSafe(m[1]), TotalObjects: atoiSafe(m[2]),
		})
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// scanLinesOrCarriageReturns is bufio.ScanLines extended to also split on
// a bare '\r', matching git's own progress redraw behavior.
func scanLinesOrCarriageReturns(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// credentialEnv prepares the environment variables git needs to
// authenticate non-interactively, and a cleanup func that removes any
// temp file it wrote. Safe to call with credential == nil (public repo).
func (c *Cloner) credentialEnv(credential *CloneCredential) ([]string, func(), error) {
	noop := func() {}
	if credential == nil {
		return nil, noop, nil
	}

	switch credential.Kind {
	case CredentialKindSSHKey:
		keyFile, err := os.CreateTemp("", "codeeditor-clone-key-*")
		if err != nil {
			return nil, noop, err
		}
		if _, err := keyFile.WriteString(credential.Secret); err != nil {
			_ = keyFile.Close()
			_ = os.Remove(keyFile.Name())
			return nil, noop, err
		}
		_ = keyFile.Close()
		if err := os.Chmod(keyFile.Name(), 0o600); err != nil {
			_ = os.Remove(keyFile.Name())
			return nil, noop, err
		}
		cleanup := func() { _ = os.Remove(keyFile.Name()) }
		sshCommand := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o IdentitiesOnly=yes", keyFile.Name())
		return []string{"GIT_SSH_COMMAND=" + sshCommand}, cleanup, nil

	case CredentialKindPAT, CredentialKindGitHubOAuthDerived:
		// GIT_ASKPASS keeps the token out of argv (visible to `ps`) — git
		// invokes this script and reads the password from its stdout.
		askpass, err := os.CreateTemp("", "codeeditor-clone-askpass-*")
		if err != nil {
			return nil, noop, err
		}
		script := fmt.Sprintf("#!/bin/sh\necho %q\n", credential.Secret)
		if _, err := askpass.WriteString(script); err != nil {
			_ = askpass.Close()
			_ = os.Remove(askpass.Name())
			return nil, noop, err
		}
		_ = askpass.Close()
		if err := os.Chmod(askpass.Name(), 0o700); err != nil {
			_ = os.Remove(askpass.Name())
			return nil, noop, err
		}
		cleanup := func() { _ = os.Remove(askpass.Name()) }
		return []string{"GIT_ASKPASS=" + askpass.Name(), "GIT_TERMINAL_PROMPT=0"}, cleanup, nil

	default:
		return nil, noop, fmt.Errorf("gitmanager: unsupported credential kind %q for clone", credential.Kind)
	}
}
