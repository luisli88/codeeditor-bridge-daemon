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
		return "pyright-langserver", []string{"--stdio"}, nil
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
