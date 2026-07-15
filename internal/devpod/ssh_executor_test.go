package devpod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHExecutor_Run_Succeeds_InvokesDevpodSSHWithWorkspaceID(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls")
	fakeDevpodScript(t, fmt.Sprintf(`
echo "$@" >> %s
exit 0
`, callsPath))

	err := SSHExecutor{WorkspaceID: "ws-1"}.Run(context.Background(), "npm", "install", "-g", "@vtsls/language-server")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	calls, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatalf("read calls log: %v", readErr)
	}
	log := string(calls)
	for _, want := range []string{"ssh ws-1", "--command", "npm", "install", "-g", "@vtsls/language-server"} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected devpod to have been called with %q, call log was:\n%s", want, log)
		}
	}
}

// InstallClaudeCode's real command is `bash -c "curl ... | bash"` — the
// pipe has to survive being rejoined into --command's single string and
// re-split by the remote shell, not get truncated at the first `|`.
func TestSSHExecutor_Run_QuotesArgsWithShellMetacharacters(t *testing.T) {
	callsPath := filepath.Join(t.TempDir(), "calls")
	fakeDevpodScript(t, fmt.Sprintf(`
echo "$@" >> %s
exit 0
`, callsPath))

	err := SSHExecutor{WorkspaceID: "ws-1"}.Run(context.Background(), "bash", "-c", "curl -fsSL https://claude.ai/install.sh | bash")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	calls, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatalf("read calls log: %v", readErr)
	}
	wantSubstring := `'bash' '-c' 'curl -fsSL https://claude.ai/install.sh | bash'`
	if !strings.Contains(string(calls), wantSubstring) {
		t.Fatalf("expected call log to contain %q, got:\n%s", wantSubstring, string(calls))
	}
}

func TestSSHExecutor_Run_FailsWithFatalLine_UsesItsMessage(t *testing.T) {
	fakeDevpodScript(t, `
echo '{"level":"fatal","message":"Process exited with status 1"}'
exit 1
`)

	err := SSHExecutor{WorkspaceID: "ws-1"}.Run(context.Background(), "npm", "install", "-g", "@vtsls/language-server")

	if err == nil {
		t.Fatal("expected an error")
	}
	wantSubstring := "Process exited with status 1"
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected error to contain %q, got %q", wantSubstring, err.Error())
	}
}
