package tests

import (
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Source files, not inline Go strings, so they read (and get syntax
// highlighted) like the real shell scripts they stand in for — see each
// file's own comment for what it does.
//
//go:embed testdata/fake_devpod_ssh.sh
var fakeDevpodSSHScriptBody string

//go:embed testdata/fake_devpod_ssh_with_noise.sh
var fakeDevpodSSHScriptWithDevpodNoiseBody string

// fakeDevpodSSHScript stands in for the real `devpod` CLI so
// devpodexec.StartPiped/StartWithOutputPipes/devpod.SSHExecutor (all of
// which shell out to `devpod ssh <workspaceID> --command <cmd> ...`) can
// be exercised without the real binary or a real Workspace — same spirit
// as fakeDevpodScript in internal/devpod/runner_test.go (a different Go
// package, so this is a separate copy rather than a shared import — see
// that file's own doc comment for why a fake script on PATH is this
// project's established way to test devpod CLI invocations).
func fakeDevpodSSHScript(t *testing.T) {
	writeFakeDevpodScript(t, fakeDevpodSSHScriptBody)
}

// fakeDevpodSSHScriptWithDevpodNoise is fakeDevpodSSHScript plus a real
// devpod ssh's own tunnel-teardown line on stderr after the command
// finishes — for asserting that noise gets filtered rather than relayed
// to the Desarrollador as if it were the program's own stderr.
func fakeDevpodSSHScriptWithDevpodNoise(t *testing.T) {
	writeFakeDevpodScript(t, fakeDevpodSSHScriptWithDevpodNoiseBody)
}

func writeFakeDevpodScript(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake devpod script is a POSIX shell script")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "devpod")
	if err := os.WriteFile(scriptPath, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake devpod script: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
