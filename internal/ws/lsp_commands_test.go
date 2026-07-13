package ws

import "testing"

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
