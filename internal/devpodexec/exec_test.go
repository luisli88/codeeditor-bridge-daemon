package devpodexec

import (
	"context"
	_ "embed"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

//go:embed testdata/fake_devpod_ssh.sh
var fakeDevpodSSHScriptBody string

func fakeDevpodSSHScript(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake devpod script is a POSIX shell script")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "devpod")
	if err := os.WriteFile(scriptPath, []byte(fakeDevpodSSHScriptBody), 0o700); err != nil {
		t.Fatalf("write fake devpod script: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestShellJoin_QuotesPartsWithMetacharacters(t *testing.T) {
	got := ShellJoin([]string{"bash", "-c", "curl -fsSL https://example.com | bash"})
	want := `'bash' '-c' 'curl -fsSL https://example.com | bash'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestShellJoin_EscapesEmbeddedSingleQuotes(t *testing.T) {
	got := ShellJoin([]string{"echo", "it's here"})
	want := `'echo' 'it'\''s here'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStartPiped_WritesToStdinAndReadsFromStdout(t *testing.T) {
	fakeDevpodSSHScript(t)

	cmd, stdin, stdout, err := StartPiped(context.Background(), "ws-1", "cat")
	if err != nil {
		t.Fatalf("StartPiped: %v", err)
	}
	defer cmd.Wait() //nolint:errcheck

	if _, err := stdin.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}

	buf := make([]byte, 6)
	if _, err := io.ReadFull(stdout, buf); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(buf) != "hello\n" {
		t.Fatalf("got %q, want %q", buf, "hello\n")
	}
}

func TestStartWithOutputPipes_StreamsStdoutAndStderrSeparately(t *testing.T) {
	fakeDevpodSSHScript(t)

	cmd, stdout, stderr, err := StartWithOutputPipes(context.Background(), "ws-1", "sh", "-c", "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("StartWithOutputPipes: %v", err)
	}

	outBytes, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	errBytes, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if string(outBytes) != "out\n" {
		t.Fatalf("stdout: got %q, want %q", outBytes, "out\n")
	}
	if string(errBytes) != "err\n" {
		t.Fatalf("stderr: got %q, want %q", errBytes, "err\n")
	}
}
