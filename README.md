# codeeditor-bridge-daemon

CodeEditor's long-lived control plane, written in **Go**. Multiplexes a single secure WebSocket to the client across the `lsp`, `shell`, `claude`, `fs`, `run`, `git`, `debug`, and `entitlements` channels, and keeps each project's `tmux` session alive across reconnects (including the interactive `claude` CLI session inside that same `tmux` — no Claude Agent SDK, see research.md §7) along with its Language Server processes.

This repository is a **submodule** of the coordinator repository [`CodeEditor`](https://github.com/luisli88/CodeEditor), which holds the specs, architecture, and constitution this component depends on. Read it first before touching this code — in particular Principio VII (why this component is an explicit exception to the rest of the backend's "one Lambda per domain" pattern) and Principio II (Terraform banned; all infrastructure via CDK/CloudFormation).

## Prerequisites

- Go 1.26+
- [DevPod CLI](https://devpod.sh/) (`devpod` on `$PATH`)
- Docker (or whatever container engine DevPod is configured to target)
- `tmux`

## Structure

```text
cmd/bridged/          # binary entrypoint — wires every real channel/HTTP handler together
internal/
├── ws/                # multiplexed WebSocket server
├── gitmanager/        # cloning to EFS before the project's container exists; SecretStore (AWS + local file-based)
├── entitlements/      # Entitlements Gate client (codeeditor-backend)
├── devpod/            # devpod CLI invocation + devcontainer.json
├── bootstrap/         # automatic install of Language Servers and debug toolchains
├── debug/             # per-language DAP adapters (debugpy, vscode-js-debug, dlv dap, java-debug, lldb-dap, CodeLLDB)
└── session/           # tmux (Terminal), one persistent session per project; detects the `claude` CLI's auth state for the `claude` channel — no Claude Agent SDK (research.md §7)
tests/
```

See `specs/001-core-development-flows/contracts/websocket-protocol.md` in the coordinator repo for the full multiplexed protocol contract.

## Configuration

`cmd/bridged` is configured today only via command-line flags (no environment variables or config file):

| Flag | Default | Description |
|---|---|---|
| `--port` | `8443` | Listen port |
| `--tls-cert` | *(required)* | Path to the TLS certificate |
| `--tls-key` | *(required)* | Path to the TLS private key |
| `--workspace-dir` | `/var/lib/codeeditor/workspaces` | Root Workspaces are cloned into (local equivalent of the EFS mount) |
| `--secrets-dir` | `/var/lib/codeeditor/secrets` | Root for `gitmanager.LocalFileStore` — self-hosted git credentials, no AWS |
| `--mosh-udp-port-range` | `60000-61000` | UDP port range the `/handshake` checklist validates |

## Running locally

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key \
  --workspace-dir ./tmp/workspaces --secrets-dir ./tmp/secrets
```

`main.go` registers every `internal/ws` channel that has a real implementation: `lsp`, `shell`, `run`, `git`, `debug` — verified serving real traffic end to end (a real clone of a public repo, real ed25519 SSH key generation, real `devcontainer.json` detection). `claude` has no handler of its own — it's pushed from the `shell` channel's `OutputWatcher` (contracts/auth-flows.md), no separate handler is needed. Neither does `entitlements` — the only caller of `Gate.CheckQuota` is the `Provisioner`, reached via `POST /projects/provision`, not a direct WS envelope (nothing in the app sends one either). `fs` still has no implementation anywhere — file-tree sync was never built, see `specs/001-core-development-flows/tasks.md` T103.

## Tests

```bash
go test ./... -cover
golangci-lint run ./...
```

Target coverage: ≥90% on `internal/entitlements`, `internal/gitmanager`, and `internal/session` — see Principio V of the coordinator repo's constitution.

## Deployment

**Self-hosted (Mac mini, VPS, your own AWS account)**: build the binary and run it as a persistent service (systemd/launchd) on a host with `tmux`, Docker (or whatever engine DevPod is configured to target), and the DevPod CLI installed.

```bash
go build -o bridged ./cmd/bridged
./bridged --port 8443 --tls-cert /etc/codeeditor/tls.crt --tls-key /etc/codeeditor/tls.key
```

**Local with Docker** (for fast iteration without systemd/launchd):

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout dev.key -out dev.crt -subj "/CN=localhost"

docker build -t codeeditor-bridge-daemon .
docker run --rm -p 8443:8443 \
  -v "$(pwd)/dev.crt:/etc/bridged/tls.crt:ro" \
  -v "$(pwd)/dev.key:/etc/bridged/tls.key:ro" \
  -v codeeditor-workspaces:/var/lib/codeeditor/workspaces \
  -v codeeditor-secrets:/var/lib/codeeditor/secrets \
  -v /var/run/docker.sock:/var/run/docker.sock \
  codeeditor-bridge-daemon
```

The two named volumes (`codeeditor-workspaces`, `codeeditor-secrets`) are optional but recommended — without them, cloned Workspaces and generated git credentials are lost every time the container is recreated.

The `Dockerfile` is the daemon's own image (Go binary + `tmux`/`git`/`ssh`/the `devpod` CLI/the `docker` CLI) — it is **not** where each project's Workspace lives. Those are created on demand by `devpod up` from each repository's own `devcontainer.json` (detected or generated, `internal/gitmanager`) — there is no, and shouldn't be, a fixed "CodeEditor workspace" image in any registry; that's exactly what using DevPod instead of maintaining per-language images solves. The host's Docker socket is mounted (not Docker-in-Docker) so `devpod` creates those Workspaces as sibling containers, not children, of the daemon's own container.

**Verified end to end in self-hosted mode** (outside the container, same binary): `/handshake` responds with the machine's real checklist; `POST /git-credentials/ssh-key/generate` generates and stores a real ed25519 key; `POST /projects/provision` actually clones a real public repository, detects/generates its `devcontainer.json`, and reaches the `devpod-up` step — which only fails because `devpod` wasn't installed on the machine this particular check was run on (it is in the Docker image, so that step runs for real there too).

**Real gap still open**: `internal/ws/lsp_proxy.go`, `internal/debug` (DAP adapters), `internal/devpod` (Run execution), and `internal/bootstrap.Executor` all invoke their subprocesses (`pyright-langserver`, `debugpy`, the Run command, `pip`/`npm`/etc.) directly on the Bridge Daemon's own host/container — not inside the Workspace container via `devpod ssh`. This doesn't show with a single active test Workspace (everything runs against the same cloned filesystem), but it's wrong for several concurrent Workspaces, the real production case. Closing this is a bigger change (routing every subprocess through `devpod ssh <workspace> -- <command>`) that wasn't in scope for this pass.

**Managed tier (AWS)**: the custom CDK stack (`backend/amplify/cdk/bridge-daemon-infra.ts`, in the coordinator's `backend/` submodule) today only provisions the shared VPC and ECS cluster — the daemon's own Service/task definition and pushing this image to ECR aren't implemented. Neither was in scope for `specs/001-core-development-flows/tasks.md`. For a managed deployment, `LocalFileStore`/`entitlements.NewGate(nil)` in `main.go` would also need to be swapped for `SecretsManagerStore`/a real `entitlements.DynamoDBStore` — the seam already exists (`gitmanager.SecretStore`, `entitlements.Store` are interfaces), only the real-AWS-client construction in `main.go` is missing.
