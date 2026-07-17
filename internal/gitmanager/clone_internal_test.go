package gitmanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloner_ListWorkspaceIDs_ReturnsOnlyDirsWithGitEntry(t *testing.T) {
	baseDir := t.TempDir()
	mustInitRepo(t, filepath.Join(baseDir, "ws-1"))
	mustInitRepo(t, filepath.Join(baseDir, "ws-2"))
	if err := os.MkdirAll(filepath.Join(baseDir, "not-a-clone"), 0o755); err != nil {
		t.Fatalf("mkdir not-a-clone: %v", err)
	}

	ids, err := NewCloner(baseDir).ListWorkspaceIDs()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	want := map[string]bool{"ws-1": true, "ws-2": true}
	if len(ids) != len(want) {
		t.Fatalf("expected %v, got %v", want, ids)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected workspaceID %q in result %v", id, ids)
		}
	}
}

func TestCloner_ListWorkspaceIDs_BaseDirMissing_ReturnsEmptyNotError(t *testing.T) {
	ids, err := NewCloner(filepath.Join(t.TempDir(), "does-not-exist")).ListWorkspaceIDs()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no workspace ids, got %v", ids)
	}
}

func TestCloner_RepositoryURL_ReturnsOriginRemote(t *testing.T) {
	baseDir := t.TempDir()
	workspacePath := filepath.Join(baseDir, "ws-1")
	mustInitRepo(t, workspacePath)
	mustRunGit(t, workspacePath, "remote", "add", "origin", "git@github.com:acme/repo.git")

	url, err := NewCloner(baseDir).RepositoryURL(workspacePath)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if url != "git@github.com:acme/repo.git" {
		t.Fatalf("expected origin remote url, got %q", url)
	}
}

func TestCloner_RepositoryURL_NoOriginRemote_ReturnsError(t *testing.T) {
	baseDir := t.TempDir()
	workspacePath := filepath.Join(baseDir, "ws-1")
	mustInitRepo(t, workspacePath)

	_, err := NewCloner(baseDir).RepositoryURL(workspacePath)

	if err == nil {
		t.Fatal("expected an error for a repo with no origin remote")
	}
}

func mustInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
	mustRunGit(t, dir, "init", "--quiet")
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"0": 0, "42": 42, "1234": 1234, "": 0, "12a": 12}
	for input, want := range cases {
		if got := atoiSafe(input); got != want {
			t.Errorf("atoiSafe(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestScanLinesOrCarriageReturns(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		atEOF   bool
		advance int
		token   string
	}{
		{"newline", "abc\ndef", false, 4, "abc"},
		{"carriage return", "abc\rdef", false, 4, "abc"},
		{"no terminator, not EOF", "abc", false, 0, ""},
		{"no terminator, at EOF", "abc", true, 3, "abc"},
		{"empty at EOF", "", true, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			advance, token, err := scanLinesOrCarriageReturns([]byte(tc.data), tc.atEOF)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if advance != tc.advance {
				t.Errorf("advance = %d, want %d", advance, tc.advance)
			}
			if string(token) != tc.token {
				t.Errorf("token = %q, want %q", token, tc.token)
			}
		})
	}
}

func TestReportCloneProgressLine(t *testing.T) {
	var reported []CloneProgress
	reporter := ReporterFunc(func(p CloneProgress) { reported = append(reported, p) })

	reportCloneProgressLine("Receiving objects:  45% (555/1234), 2.34 MiB | 1.02 MiB/s", reporter)
	reportCloneProgressLine("Resolving deltas:  10% (50/500)", reporter)
	reportCloneProgressLine("Cloning into 'foo'...", reporter)

	if len(reported) != 2 {
		t.Fatalf("expected 2 progress reports, got %d: %+v", len(reported), reported)
	}
	if reported[0].ObjectsReceived != 555 || reported[0].TotalObjects != 1234 {
		t.Errorf("unexpected receiving-objects progress: %+v", reported[0])
	}
	if reported[1].DeltasResolved != 50 || reported[1].TotalObjects != 500 {
		t.Errorf("unexpected resolving-deltas progress: %+v", reported[1])
	}
}

func TestCredentialEnv_NilCredential_ReturnsNoEnv(t *testing.T) {
	env, cleanup, err := credentialEnv(nil)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Errorf("expected nil env for a public repo, got %v", env)
	}
}

func TestCredentialEnv_SSHKey_WritesTempKeyFileAndSetsGitSSHCommand(t *testing.T) {
	env, cleanup, err := credentialEnv(&CloneCredential{Kind: CredentialKindSSHKey, Secret: "fake-key-material"})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || !strings.Contains(env[0], "GIT_SSH_COMMAND=") {
		t.Errorf("expected a single GIT_SSH_COMMAND env var, got %v", env)
	}
}

func TestCredentialEnv_PAT_WritesAskpassScript(t *testing.T) {
	env, cleanup, err := credentialEnv(&CloneCredential{Kind: CredentialKindPAT, Secret: "ghp_token"})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range env {
		if strings.Contains(e, "GIT_ASKPASS=") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a GIT_ASKPASS env var, got %v", env)
	}
}

func TestCredentialEnv_UnsupportedKind_ReturnsError(t *testing.T) {
	_, cleanup, err := credentialEnv(&CloneCredential{Kind: CredentialKind("unknown"), Secret: "x"})
	defer cleanup()
	if err == nil {
		t.Fatal("expected an error for an unsupported credential kind")
	}
}
