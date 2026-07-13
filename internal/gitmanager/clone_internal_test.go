package gitmanager

import (
	"strings"
	"testing"
)

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
