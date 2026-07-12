package bootstrap

import "context"

// InstallClaudeCode installs the Claude Code CLI inside the project's
// container, without authenticating it — FR-027's "sesión inicial del
// agente de codificación" only requires `claude` to be present and
// runnable; authentication happens later, bridged over the `claude`
// WebSocket channel the first time the Desarrollador opens that panel
// (contracts/auth-flows.md).
func InstallClaudeCode(ctx context.Context, exec Executor) error {
	return exec.Run(ctx, "bash", "-c", "curl -fsSL https://claude.ai/install.sh | bash")
}
