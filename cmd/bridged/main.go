// Command bridged is the Bridge Daemon entrypoint — the persistent control
// plane described in the CodeEditor coordinator repo's constitution
// (Principio VII) and specs/001-core-development-flows/plan.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/bootstrap"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/debug"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/devpod"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/entitlements"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/filetree"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/session"
	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

func main() {
	port := flag.Int("port", 8443, "port to listen on")
	tlsCert := flag.String("tls-cert", "", "path to TLS certificate")
	tlsKey := flag.String("tls-key", "", "path to TLS private key")
	workspaceDir := flag.String("workspace-dir", "/var/lib/codeeditor/workspaces", "root directory Workspaces are cloned into")
	secretsDir := flag.String(
		"secrets-dir", "/var/lib/codeeditor/secrets",
		"root directory git credentials are stored in (gitmanager.LocalFileStore) — self-hosted only, "+
			"see README.md \"Despliegue\" for the managed-tier (AWS Secrets Manager) gap this doesn't cover",
	)
	moshPortRange := flag.String("mosh-udp-port-range", "60000-61000", "UDP port range mosh-server is configured for")
	flag.Parse()

	if *tlsCert == "" || *tlsKey == "" {
		log.Fatal("bridged: --tls-cert and --tls-key are required")
	}

	cloner := gitmanager.NewCloner(*workspaceDir)
	secretStore := gitmanager.NewLocalFileStore(*secretsDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", ws.HandshakeHandler(ws.DefaultChecklist(*moshPortRange)))
	wireGitCredentials(mux, secretStore)
	wireProvisioning(mux, cloner, secretStore)
	mux.Handle("/", wireChannels(cloner, secretStore))

	log.Printf("bridged: listening on :%d", *port)
	log.Fatal(http.ListenAndServeTLS(":"+strconv.Itoa(*port), *tlsCert, *tlsKey, mux))
}

// wireGitCredentials mounts the git-credentials HTTP surface
// (BridgeGitCredentialsService on the app side). LocalFileStore, not
// SecretsManagerStore: self-hosted has no AWS account of its own
// (Principio II), and SecretsManagerStore was, until now, the only
// SecretStore implementation, making even pure self-hosted git
// credentials a hard AWS dependency.
func wireGitCredentials(mux *http.ServeMux, store gitmanager.SecretStore) {
	validator := gitmanager.NewProviderValidator()
	manager := gitmanager.NewCredentialManager(store, validator)
	gitmanager.RegisterRoutes(mux, manager)
}

// wireProvisioning mounts POST /projects/provision and /projects/retry-step
// (BridgeProvisioningService). The Entitlements Gate is built with a nil
// Store: CheckQuota never touches it for HostKind == "self-hosted"
// (internal/entitlements/gate.go), which is the only path this wiring
// supports end to end today — a managed-tier deployment needs a real
// entitlements.DynamoDBStore here instead.
func wireProvisioning(mux *http.ServeMux, cloner *gitmanager.Cloner, secretStore gitmanager.SecretStore) {
	gate := entitlements.NewGate(nil)

	provisioner := devpod.NewProvisioner(
		gate,
		cloner.WorkspacePath,
		cloneFunc(cloner),
		secretStore.Get,
		devcontainerFunc,
		gitmanager.DetectLanguages,
		devPodUpFunc,
		lspInstallFunc,
		claudeInstallFunc,
	)
	devpod.RegisterRoutes(mux, provisioner)
}

// wireChannels builds the multiplexed WebSocket server (contracts/
// websocket-protocol.md) for every channel with a real implementation.
// Not registered: `claude` (server-initiated pushes only, delivered via
// the `shell` channel's OutputWatcher below — contracts/auth-flows.md).
// `entitlements` is also not a direct channel here: the only caller of
// Gate.CheckQuota is Provisioner, reached over the HTTP provisioning
// routes above, not a raw WS envelope — nothing in the app sends one
// either (EntitlementsService.swift queries Amplify.API directly).
func wireChannels(cloner *gitmanager.Cloner, secretStore gitmanager.SecretStore) *ws.Server {
	server := ws.NewServer()

	lspProxy := ws.NewLSPProxy(ws.DefaultLSPCommand, ensureLSPInstalledFunc())
	server.Handle(ws.ChannelLSP, lspProxy.Handler())

	shellSessions := session.NewShellSessions(tmuxSessionName)
	shellSessions.SetOutputWatcher(session.NewAuthDetector().Watch())
	server.Handle(ws.ChannelShell, shellSessions.Handler())

	runManager := devpod.NewRunManager(previewURLFunc)
	server.Handle(ws.ChannelRun, runManager.Handler())

	operations := gitmanager.NewOperations(cloner.WorkspacePath)
	server.Handle(ws.ChannelGit, operations.Handler(secretStore.Get))

	debugProxy := debug.NewProxy(debug.DefaultAdapterCommand)
	server.Handle(ws.ChannelDebug, debugProxy.Handler())

	fsBrowser := filetree.NewBrowser(cloner.WorkspacePath)
	server.Handle(ws.ChannelFS, fsBrowser.Handler())

	return server
}

func tmuxSessionName(workspaceID string) string { return "codeeditor-" + workspaceID }

func previewURLFunc(workspaceID string) string {
	// Real DNS/reverse-proxy routing for this URL is infrastructure this
	// package doesn't own or verify (devpod/run.go's own doc comment).
	return fmt.Sprintf("https://%s.workspaces.local", workspaceID)
}

func cloneFunc(cloner *gitmanager.Cloner) devpod.CloneFunc {
	return func(ctx context.Context, workspaceID, repositoryURL string, credential *devpod.CloneCredential) error {
		var gmCredential *gitmanager.CloneCredential
		if credential != nil {
			gmCredential = &gitmanager.CloneCredential{Kind: gitmanager.CredentialKind(credential.Kind), Secret: credential.Secret}
		}
		noopReporter := gitmanager.ReporterFunc(func(gitmanager.CloneProgress) {})
		return cloner.Clone(ctx, workspaceID, repositoryURL, gmCredential, noopReporter)
	}
}

func devcontainerFunc(workspacePath string) (map[string]any, error) {
	cfg, err := gitmanager.DetectOrGenerate(workspacePath)
	return map[string]any(cfg), err
}

func devPodUpFunc(ctx context.Context, workspacePath string) error {
	return devpod.SubprocessRunner{}.Up(ctx, workspacePath, func(line string) { log.Printf("devpod up: %s", line) })
}

// lspInstallFunc/claudeInstallFunc build a fresh devpod.SSHExecutor per
// call rather than sharing one — installing has to happen *inside* the
// just-created Workspace container (`devpod ssh <workspaceID>`), never
// on the Bridge Daemon's own host, and which Workspace that is isn't
// known until Provisioner calls in with a specific workspaceID.
func lspInstallFunc(ctx context.Context, workspaceID string, languages []string) []bootstrap.LanguageServer {
	bootstrapper := bootstrap.NewLSPBootstrapper(devpod.SSHExecutor{WorkspaceID: workspaceID})
	return bootstrapper.Install(ctx, languages, false)
}

// ensureLSPInstalledFunc, like lspInstallFunc above, builds a fresh
// SSHExecutor per call (targeting whichever Workspace the `lsp` channel
// call is actually for) rather than once at wiring time — FR-036's
// on-demand install needs to land inside that specific Workspace
// container too, not the Bridge Daemon's own host.
func ensureLSPInstalledFunc() ws.EnsureInstalledFunc {
	return func(ctx context.Context, workspaceID, languageID string) error {
		bootstrapper := bootstrap.NewLSPBootstrapper(devpod.SSHExecutor{WorkspaceID: workspaceID})
		servers := bootstrapper.Install(ctx, []string{languageID}, true)
		if len(servers) > 0 && servers[0].Status == bootstrap.LanguageServerStatusError {
			return fmt.Errorf("bridged: install Language Server for %q failed", languageID)
		}
		return nil
	}
}

func claudeInstallFunc(ctx context.Context, workspaceID string) error {
	return bootstrap.InstallClaudeCode(ctx, devpod.SSHExecutor{WorkspaceID: workspaceID})
}
