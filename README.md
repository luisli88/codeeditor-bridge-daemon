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

Doing all of this by hand is exactly what `app/Sources/Features/RemoteSetup/RemoteSetupService.swift` automates over SSH from the app itself — it downloads a prebuilt binary instead of building one (`.github/workflows/release.yml` publishes `bridged-linux-{amd64,arm64}` as GitHub Release assets on every tag; this repo is public specifically so those assets are reachable with no credentials from a fresh host) and sets it up as a systemd service (falling back to a detached process if the host has no systemd). See that file's doc comment for the exact scope (apt-only, root/passwordless-sudo required).

**Testing the app's actual connection flow against something SSH-reachable**: `dev/fake-remote-host/` — a container that simulates a bare self-hosted machine (SSH access, Docker, `mosh-server`) instead of just running this Dockerfile with its port published straight to `localhost`. You SSH in and set the daemon up yourself, exactly like the self-hosted steps above, against a fake VPS instead of a real one — see `dev/fake-remote-host/README.md`. Verified end to end: real SSH password login, `docker info` from inside it against the real host's Docker (after fixing a docker.sock GID mismatch — see that README), and all three `/handshake` checklist items (`bridge-daemon-reachable`, `container-engine`, `mosh`) come back `verified`, both from inside the container and through the app's own connection.

**Local with Docker** (for fast iteration without systemd/launchd):

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout dev.key -out dev.crt -subj "/CN=localhost"

mkdir -p "$HOME/codeeditor-data/workspaces" "$HOME/codeeditor-data/secrets"

docker build -t codeeditor-bridge-daemon .
docker run --rm -p 8443:8443 \
  -v "$(pwd)/dev.crt:/etc/bridged/tls.crt:ro" \
  -v "$(pwd)/dev.key:/etc/bridged/tls.key:ro" \
  -v "$HOME/codeeditor-data/workspaces:$HOME/codeeditor-data/workspaces" \
  -v "$HOME/codeeditor-data/secrets:$HOME/codeeditor-data/secrets" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  codeeditor-bridge-daemon \
  --workspace-dir "$HOME/codeeditor-data/workspaces" \
  --secrets-dir "$HOME/codeeditor-data/secrets"
```

`$HOME/codeeditor-data/{workspaces,secrets}` are bind-mounted from the **real host**, at that same identical path — not named volumes, and this matters beyond just surviving a container recreate: `devpod up` (invoked from inside this container, against the real Docker host via the mounted socket) asks that real host to bind-mount a Workspace's path into the sibling container it creates. A named volume's real path (`/var/lib/docker/volumes/<name>/_data`) isn't the path `devpod` asks for, so `devpod up` fails with `bind source path does not exist: <path>/<id>` — confirmed by hand against a real `devpod up`, not just reasoned through. Same identical path on both sides (host and container) is what makes the bind-mount source real from dockerd's point of view.

`$HOME` (rather than the `--workspace-dir`/`--secrets-dir` defaults, `/var/lib/codeeditor/{workspaces,secrets}` — still the right default for a real self-hosted machine, see "Self-hosted" above) is what lets the `mkdir` above skip `sudo` for local iteration on your own Mac — but it's not an arbitrary sudo-free path either. On Colima specifically (`docker context ls` shows which backend a given machine is on), the VM's virtiofs mount only maps UID/GID correctly under the real `$HOME`; a path like `/Users/Shared`, while world-writable on macOS itself, mounts into the Colima VM owned `root:root` and unwritable by any other UID — confirmed by hand, cost a failed provisioning run to find.

The `Dockerfile` is the daemon's own image (Go binary + `tmux`/`git`/`ssh`/the `devpod` CLI/the `docker` CLI) — it is **not** where each project's Workspace lives. Those are created on demand by `devpod up` from each repository's own `devcontainer.json` (detected or generated, `internal/gitmanager`) — there is no, and shouldn't be, a fixed "CodeEditor workspace" image in any registry; that's exactly what using DevPod instead of maintaining per-language images solves. The host's Docker socket is mounted (not Docker-in-Docker) so `devpod` creates those Workspaces as sibling containers, not children, of the daemon's own container.

**Verified end to end in self-hosted mode** (outside the container, same binary): `/handshake` responds with the machine's real checklist; `POST /git-credentials/ssh-key/generate` generates and stores a real ed25519 key; `POST /projects/provision` actually clones a real public repository, detects/generates its `devcontainer.json`, brings up a real Workspace, installs its Language Server(s), and installs the Claude Code CLI — `Result.Status` reaches `"ready"` for real, every step `"done"`. Real, confirmed bugs found and fixed getting there:

- `SubprocessRunner` invoked `devpod up --output json`, a flag that doesn't exist (`--log-output json` is the real one — every single call failed immediately with `unknown flag: --output`).
- A fresh `devpod` install has no provider configured at all (`Up` now runs `devpod provider add/use docker` itself, once, before the first real `up`).
- The bind-mount path-parity issue above (Docker-outside-of-Docker).
- `gitmanager.Cloner.Clone` discarded git's own stderr on failure, returning a bare `exit status 128` — now surfaces git's actual last stderr line (e.g. `fatal: repository ... not found`).
- `gitmanager.DetectOrGenerate` computed a devcontainer config but never wrote it to `.devcontainer/devcontainer.json` — `devpod up` only ever reads that file off disk, so it silently ignored the detection and ran its own (cruder, sometimes wrong) auto-detection instead. Now writes the generated file when none exists on disk already.
- `bootstrap.SubprocessExecutor` (LSP install, Claude Code install) ran `npm`/`curl` directly on the Bridge Daemon's own host — which not only lacks those toolchains but wouldn't install into the right container even if it did. `devpod.SSHExecutor` now runs them via `devpod ssh <workspaceID> --command ...`, the same pattern `Up` already used for `devpod up` itself.

`internal/devpod.ProvisioningStep.Error` carries the actual failure message for whichever step still fails (devpod's own `{"level":"fatal","message":...}` line when there is one, falling back to stderr) — a failed step used to be a bare red X with nothing to explain it.

**The `Dockerfile` itself is also verified** with a real `docker build`/`docker run` (not just reasoned through) — `/handshake` responded `bridge-daemon-reachable: verified` from the built image. One real fix that came out of it: bind-mounting a single file to a path whose parent directory doesn't exist yet in the image can make Docker create a *directory* there instead (hit this on Docker Desktop for Mac) — `/etc/bridged` is now pre-created in the image to avoid it.

**Real gap still open**: `internal/ws/lsp_proxy.go` (the live Language Server process, once `bootstrap.Executor` above has installed it), `internal/debug` (DAP adapters), and `internal/devpod` (Run execution) all still invoke their subprocesses (`pyright-langserver`, `debugpy`, the Run command) directly on the Bridge Daemon's own host/container — not inside the Workspace container via `devpod ssh`. `internal/bootstrap.Executor` no longer has this problem (see `devpod.SSHExecutor` above) — what's left is routing a *live, long-running* process's stdio through a `devpod ssh` tunnel instead of a run-to-completion install command, a materially bigger change than SSHExecutor's one-shot `--command` calls (`LSP`/debug need an interactively piped subprocess, not one `exec.Command().Run()`). This doesn't show with a single active test Workspace (everything runs against the same cloned filesystem), but it's wrong for several concurrent Workspaces, the real production case.

**Managed tier (AWS)**: the custom CDK stack (`backend/amplify/cdk/bridge-daemon-infra.ts`, in the coordinator's `backend/` submodule) today only provisions the shared VPC and ECS cluster — the daemon's own Service/task definition and pushing this image to ECR aren't implemented. Neither was in scope for `specs/001-core-development-flows/tasks.md`. For a managed deployment, `LocalFileStore`/`entitlements.NewGate(nil)` in `main.go` would also need to be swapped for `SecretsManagerStore`/a real `entitlements.DynamoDBStore` — the seam already exists (`gitmanager.SecretStore`, `entitlements.Store` are interfaces), only the real-AWS-client construction in `main.go` is missing.
