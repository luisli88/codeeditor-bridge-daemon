package devpod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeDevpodScript writes a shell script named "devpod" that stands in
// for the real CLI and prepends its directory to PATH for this test's
// subprocess calls — SubprocessRunner shells out to "devpod" by name
// (research.md §3: no stable Go SDK exists to call instead), so this is
// the only way to exercise its stdout/stderr/exit-code handling without
// the real binary installed.
func fakeDevpodScript(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake devpod script is a POSIX shell script")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "devpod")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("write fake devpod script: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSubprocessRunner_Up_Succeeds_ForwardsEachLineToOnEvent(t *testing.T) {
	fakeDevpodScript(t, `
if [ "$1" = "provider" ]; then
  if [ "$2" = "list" ]; then echo "NAME"; echo "docker"; exit 0; fi
fi
echo '{"level":"info","message":"Creating devcontainer..."}'
echo '{"level":"info","message":"Done"}'
exit 0
`)

	var lines []string
	err := SubprocessRunner{}.Up(context.Background(), "/tmp/workspace", func(line string) {
		lines = append(lines, line)
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines forwarded to onEvent, got %d: %v", len(lines), lines)
	}
}

func TestSubprocessRunner_Up_FailsWithFatalLine_UsesItsMessage(t *testing.T) {
	fakeDevpodScript(t, `
if [ "$1" = "provider" ] && [ "$2" = "list" ]; then echo "NAME"; echo "docker"; exit 0; fi
echo '{"level":"info","message":"Creating devcontainer..."}'
echo '{"level":"fatal","message":"docker: Error response from daemon: invalid mount config"}'
exit 1
`)

	err := SubprocessRunner{}.Up(context.Background(), "/tmp/workspace", func(string) {})

	if err == nil {
		t.Fatal("expected an error")
	}
	wantSubstring := "invalid mount config"
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error to contain %q, got %q", wantSubstring, err.Error())
	}
}

func TestSubprocessRunner_Up_FailsWithNoFatalLine_FallsBackToStderr(t *testing.T) {
	fakeDevpodScript(t, `
if [ "$1" = "provider" ] && [ "$2" = "list" ]; then echo "NAME"; echo "docker"; exit 0; fi
echo "panic: something unexpected" 1>&2
exit 1
`)

	err := SubprocessRunner{}.Up(context.Background(), "/tmp/workspace", func(string) {})

	if err == nil {
		t.Fatal("expected an error")
	}
	wantSubstring := "panic: something unexpected"
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error to contain %q, got %q", wantSubstring, err.Error())
	}
}

func TestSubprocessRunner_Up_NoProviderConfigured_AddsAndUsesDocker(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls")
	fakeDevpodScript(t, fmt.Sprintf(`
echo "$@" >> %s
if [ "$1" = "provider" ] && [ "$2" = "list" ]; then echo "NAME"; exit 0; fi
if [ "$1" = "provider" ] && [ "$2" = "add" ]; then exit 0; fi
if [ "$1" = "provider" ] && [ "$2" = "use" ]; then exit 0; fi
echo '{"level":"info","message":"ok"}'
exit 0
`, callsPath))

	err := SubprocessRunner{}.Up(context.Background(), "/tmp/workspace", func(string) {})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	calls, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatalf("read calls log: %v", readErr)
	}
	log := string(calls)
	for _, want := range []string{"provider list", "provider add docker", "provider use docker"} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected devpod to have been called with %q, call log was:\n%s", want, log)
		}
	}
}

func TestSubprocessRunner_Delete_Succeeds_CallsDevpodDeleteWithForceAndIgnoreNotFound(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls")
	fakeDevpodScript(t, fmt.Sprintf(`
echo "$@" >> %s
exit 0
`, callsPath))

	err := SubprocessRunner{}.Delete(context.Background(), "ws-1")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	calls, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatalf("read calls log: %v", readErr)
	}
	log := string(calls)
	for _, want := range []string{"delete ws-1", "--force", "--ignore-not-found"} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected devpod to have been called with %q, call log was:\n%s", want, log)
		}
	}
}

func TestSubprocessRunner_Delete_FailsWithFatalLine_UsesItsMessage(t *testing.T) {
	fakeDevpodScript(t, `
echo '{"level":"fatal","message":"workspace ws-1 is busy"}'
exit 1
`)

	err := SubprocessRunner{}.Delete(context.Background(), "ws-1")

	if err == nil {
		t.Fatal("expected an error")
	}
	wantSubstring := "workspace ws-1 is busy"
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error to contain %q, got %q", wantSubstring, err.Error())
	}
}

