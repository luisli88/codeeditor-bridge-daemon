package bootstrap

import "context"

// LanguageServer mirrors data-model.md → Proyecto.languageServers.
type LanguageServer struct {
	Language          string `json:"language"`
	Status            string `json:"status"` // "installing" | "ready" | "error"
	InstalledOnDemand bool   `json:"installedOnDemand"`
}

const (
	LanguageServerStatusInstalling = "installing"
	LanguageServerStatusReady      = "ready"
	LanguageServerStatusError      = "error"
)

// installCommand returns the command that installs language's Language
// Server (research.md §10): pyright (Python), vtsls (JS/TS),
// rust-analyzer (Rust), gopls (Go), clangd (C/C++), jdt.ls (Java).
// sourcekit-lsp ships with the Swift toolchain — nothing to install.
func installCommand(language string) (name string, args []string, needsInstall bool) {
	switch language {
	case "python":
		return "pip", []string{"install", "--user", "pyright"}, true
	case "javascript", "typescript":
		return "npm", []string{"install", "-g", "@vtsls/language-server"}, true
	case "rust":
		return "rustup", []string{"component", "add", "rust-analyzer"}, true
	case "go":
		return "go", []string{"install", "golang.org/x/tools/gopls@latest"}, true
	case "c", "cpp":
		return "apt-get", []string{"install", "-y", "clangd"}, true
	case "java":
		return "bash", []string{"-c",
			"curl -fsSL https://download.eclipse.org/jdtls/snapshots/jdt-language-server-latest.tar.gz " +
				"| tar -xz -C /opt/jdtls",
		}, true
	case "swift":
		return "", nil, false
	default:
		return "", nil, false
	}
}

// LSPBootstrapper installs Language Servers via an Executor — real,
// working orchestration logic, unit-tested with a fake Executor; nobody
// has run this against a live container with `pip`/`npm`/`rustup`/`go`/
// `apt-get` reachable yet, so treat the *installation itself* as
// unverified in this environment, same as the Bridge Daemon's other
// external-process integrations.
type LSPBootstrapper struct {
	exec Executor
}

func NewLSPBootstrapper(exec Executor) *LSPBootstrapper {
	return &LSPBootstrapper{exec: exec}
}

// Install installs the Language Server for every language in languages —
// called once per detected language during provisioning (FR-027,
// onDemand=false), or once for a single language opened without prior
// detection (FR-036, onDemand=true).
func (b *LSPBootstrapper) Install(ctx context.Context, languages []string, onDemand bool) []LanguageServer {
	servers := make([]LanguageServer, 0, len(languages))
	for _, language := range languages {
		servers = append(servers, b.installOne(ctx, language, onDemand))
	}
	return servers
}

func (b *LSPBootstrapper) installOne(ctx context.Context, language string, onDemand bool) LanguageServer {
	server := LanguageServer{Language: language, InstalledOnDemand: onDemand}

	name, args, needsInstall := installCommand(language)
	if !needsInstall {
		server.Status = LanguageServerStatusReady
		return server
	}

	if err := b.exec.Run(ctx, name, args...); err != nil {
		server.Status = LanguageServerStatusError
		return server
	}
	server.Status = LanguageServerStatusReady
	return server
}
