package ws

import (
	"strings"
	"testing"
)

func TestDefaultLSPCommand_KnownLanguages_ReturnRunnableCommand(t *testing.T) {
	for _, language := range []string{"python", "javascript", "typescript", "rust", "go", "c", "cpp", "swift", "java"} {
		name, _, err := DefaultLSPCommand(language)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", language, err)
		}
		if name == "" {
			t.Errorf("%s: expected a non-empty command name", language)
		}
	}
}

func TestDefaultLSPCommand_UnknownLanguage_ReturnsError(t *testing.T) {
	if _, _, err := DefaultLSPCommand("cobol"); err == nil {
		t.Error("expected an error for an unsupported language, got nil")
	}
}

// Confirmed live against a real devcontainer: pyright-langserver installs
// to $HOME/.local/bin (pip install --user), which isn't on PATH for a
// non-interactive `devpod ssh --command` session — a bare command name
// would fail to launch even though the binary genuinely exists.
func TestDefaultLSPCommand_Python_ExtendsPathForPipUserInstall(t *testing.T) {
	name, args, err := DefaultLSPCommand("python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "sh" {
		t.Errorf("expected python's Language Server to run through a shell so $HOME/$PATH expand, got name %q", name)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "$HOME/.local/bin") {
		t.Errorf("expected the command to add $HOME/.local/bin to PATH, got args %q", args)
	}
	if !strings.Contains(joined, "pyright-langserver --stdio") {
		t.Errorf("expected the command to still launch pyright-langserver --stdio, got args %q", args)
	}
}
