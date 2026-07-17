package ws

import "fmt"

// DefaultLSPCommand maps each research.md §10 language to its real
// Language Server binary invocation: pyright-langserver (Python), vtsls
// (JS/TS), rust-analyzer (Rust), gopls (Go), clangd (C/C++),
// sourcekit-lsp (Swift, ships with the toolchain), the jdt.ls launcher
// (Java). Nobody has run this against any of these servers actually
// installed in this environment, so treat the specific binaries/args as
// unverified — same category as the Bridge Daemon's other
// external-process integrations (bootstrap.installCommand,
// debug.DefaultAdapterCommand).
func DefaultLSPCommand(languageID string) (string, []string, error) {
	switch languageID {
	case "python":
		// bootstrap.installCommand installs pyright via `pip install
		// --user`, which puts pyright-langserver in $HOME/.local/bin —
		// confirmed live against a real devcontainer that this directory
		// is NOT on PATH for a non-interactive `devpod ssh --command`
		// session (unlike npm -g/rustup/go install's targets, which are
		// already there), so a bare `pyright-langserver` fails to launch
		// even though the binary is genuinely installed. `devpod ssh
		// --command` re-interprets its value through a shell inside the
		// Workspace (devpodexec.ShellJoin's doc comment), so `sh -c` here
		// gets real $HOME/$PATH expansion, not a literal string.
		return "sh", []string{"-c", "PATH=\"$HOME/.local/bin:$PATH\" exec pyright-langserver --stdio"}, nil
	case "javascript", "typescript":
		return "vtsls", []string{"--stdio"}, nil
	case "rust":
		return "rust-analyzer", nil, nil
	case "go":
		return "gopls", nil, nil
	case "c", "cpp":
		return "clangd", nil, nil
	case "swift":
		return "sourcekit-lsp", nil, nil
	case "java":
		return "java", []string{"-jar", "/opt/jdtls/plugins/org.eclipse.equinox.launcher.jar"}, nil
	default:
		return "", nil, fmt.Errorf("ws: no Language Server known for %q", languageID)
	}
}
