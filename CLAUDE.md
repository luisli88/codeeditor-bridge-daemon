This repo is a submodule of the coordinator repository [`CodeEditor`](https://github.com/luisli88/CodeEditor) — specs, architecture, and the project constitution live there (`specs/001-core-development-flows/`, `.specify/memory/constitution.md`). Read it before touching this code, in particular Principio VII (why this component is the sole exception to the rest of the backend's Lambda-per-domain pattern) and Principio II (Terraform banned).

## Working here

- One Go package per responsibility under `internal/` (`ws`, `gitmanager`, `entitlements`, `devpod`, `bootstrap`, `debug`, `session`) — no duplication between packages (Principio IV).
- Testable seam in every package that touches external infrastructure: an interface (`SecretStore`, `DynamoDBAPI`, `SSHCommandRunning`, etc.) + a fake in tests, a real implementation in production. Prefer tests against real infrastructure (real `git`/`tmux`/`ssh-keygen`, `httptest.NewTLSServer`) over lightweight mocks — see the existing tests in `tests/` and `internal/*/*_test.go` as the reference pattern.
- Target coverage ≥90% on `internal/{entitlements,gitmanager,session}` (Principio V's explicit extension to the Bridge Daemon).
- `claude` (the CLI) runs inside the Terminal's own `tmux` session — never headless/autonomous (`-p` or equivalent flags) by default (FR-049). There's a structural test (`tests/session_claude_test.go`) that fails if any file invokes `claude` with `-p`.
- `cmd/bridged/main.go` already wires everything that has a real implementation: the `lsp`/`shell`/`run`/`git`/`debug` channels, and the handshake/git-credentials/provisioning HTTP routes — verified serving real traffic end to end against self-hosted (no AWS: `gitmanager.LocalFileStore` instead of `SecretsManagerStore`, `entitlements.Gate` built with a `nil` store since `CheckQuota` never touches it for `HostKind == "self-hosted"`). Not registered: `claude` (pushed from `shell`'s `OutputWatcher`, no handler of its own needed), `entitlements` (only called internally via `Provisioner`, no caller sends a direct WS envelope), `fs` (no implementation anywhere — never built, see `specs/001-core-development-flows/tasks.md` T103).
- **Real gap still open**: `internal/ws/lsp_proxy.go` (the live Language Server process), `internal/debug`, and `internal/devpod` (Run) run their subprocesses directly on the Bridge Daemon's own host/container, not inside the Workspace container via `devpod ssh`. `internal/bootstrap.Executor` (LSP/Claude Code install, during provisioning) no longer has this problem — see `devpod.SSHExecutor`, which runs `devpod ssh <workspaceID> --command ...`. What's left needs a *live, piped* subprocess through a `devpod ssh` tunnel instead of a one-shot install command — a bigger change than SSHExecutor's. Fine for one test Workspace at a time; wrong for several concurrent ones (the real production case). See `README.md` "Deployment".
- For the managed tier (real AWS) you'd need to swap `LocalFileStore`/`Gate(nil)` for `SecretsManagerStore`/a real `entitlements.DynamoDBStore` — the seam already exists (`gitmanager.SecretStore`, `entitlements.Store` are interfaces), only the real-AWS-client construction in `main.go` is missing.
- This repo is public and `.github/workflows/release.yml` publishes `bridged-linux-{amd64,arm64}` as GitHub Release assets on every `v*` tag — `app`'s `RemoteSetupService` downloads these directly (no credentials needed) to set up a fresh self-hosted machine without requiring Docker for `bridged` itself. Needs `permissions: contents: write` on the workflow (the default `GITHUB_TOKEN` can't create releases otherwise).

## Commands

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key \
  --workspace-dir ./tmp/workspaces --secrets-dir ./tmp/secrets
go test ./... -cover
golangci-lint run ./...
docker build -t codeeditor-bridge-daemon .   # see README.md "Deployment" for docker run
```
