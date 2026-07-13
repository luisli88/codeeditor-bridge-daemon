package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-049: `claude` never runs in headless/autonomous mode by default —
// checked structurally, at the codebase level, rather than by observing
// a live process, since nothing in this module ever spawns `claude`
// outside the interactive tmux PTY session/shell.go manages (the CLI is
// only installed by bootstrap/claude.go, never invoked programmatically
// there or anywhere else). This test guards against a future regression
// introducing such an invocation: nowhere may an `exec.Command`-style
// call combine "claude" with a "-p" argument in the same file.
func TestClaudeNeverInvokedWithHeadlessFlag(t *testing.T) {
	root := "../internal"
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		content, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		text := string(content)

		if strings.Contains(text, `"claude"`) && strings.Contains(text, `"-p"`) {
			t.Errorf("%s references both \"claude\" and \"-p\" — verify claude is never invoked headless", path)
		}
		return nil
	})
	require.NoError(t, err)
}
