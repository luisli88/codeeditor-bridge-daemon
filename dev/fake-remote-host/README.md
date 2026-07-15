# fake-remote-host

A bare, SSH-reachable container that stands in for a real self-hosted machine (VPS, Mac mini) — for testing the app's UF-001 connection flow (`HostConnectionView`) end to end, including the real `/handshake` checklist, instead of just running `../../Dockerfile` (the daemon's own image) with its port published straight to `localhost`.

**This does not auto-start the Bridge Daemon.** You SSH in and set it up yourself, the same steps a real self-hosted developer would follow (`../../README.md` "Self-hosted") — that's the point: it simulates the *machine*, not the daemon already running on it.

**What this container's SSH access is *not* used for**: the app's own connection flow. UF-001's `/handshake` check is a plain HTTPS request straight to the Bridge Daemon's TLS port — no SSH involved. This container's SSH server exists for you to set the daemon up over, and later for the Terminal/`mosh` feature (`MoshTerminalService.SSHCommandRunning` — currently only a test-only fake, see `../../README.md`).

## Start it

```bash
# One-time, and only for Colima (`docker context ls` shows which backend
# you're on): its dockerd runs *inside a Linux VM*, not on macOS
# directly, and `devpod up` (below) needs this exact path to be real
# *inside that VM's own filesystem* — Colima's default `mounts: []` only
# auto-shares $HOME into the VM, /var/lib isn't under it. This needs no
# macOS admin password — only whatever `colima ssh` itself needs, since
# the VM's default user has passwordless sudo for its own filesystem
# (confirmed by hand). On Docker Desktop instead, run the same two lines
# without `colima ssh --` — there's no VM indirection there, so the path
# just needs to be real on macOS itself.
colima ssh -- sudo mkdir -p /var/lib/codeeditor/workspaces /var/lib/codeeditor/secrets
colima ssh -- sudo chown -R $(id -u):$(id -g) /var/lib/codeeditor

cd dev/fake-remote-host
docker compose up --build -d
```

This publishes `localhost:2222` → the container's SSH (port 22) and reserves `localhost:8443` for the Bridge Daemon once you start it (below). `/var/lib/codeeditor/{workspaces,secrets}` — `bridged`'s own compiled-in `--workspace-dir`/`--secrets-dir` defaults, deliberately not overridden anywhere below — are bind-mounted from the **real Docker host**, at that same identical path, not named volumes. `devpod up` (run from inside this container against the real Docker host via the mounted socket) asks that real host to bind-mount a Workspace's path into the sibling container it creates, resolved against *that host's own filesystem* using the literal path string `bridged` cloned into; a named volume's real path (`/var/lib/docker/volumes/<name>/_data`) is not that string, so `devpod up` fails with `bind source path does not exist: <path>/<id>` — confirmed by hand, not just reasoned through, including once against a real guided-setup-wizard deployment before this doc used the right path.

## SSH in and set the daemon up

```bash
ssh developer@localhost -p 2222   # password: codeeditor
```

Inside the SSH session — this is exactly `../../README.md`'s "Self-hosted" flow, just against this fake machine instead of a real one (and exactly what `app`'s guided setup wizard, `RemoteSetupService`, automates — this container needs to behave identically to a real machine for that to actually be tested). `bridged` is already built into the image at `/usr/local/bin/bridged` (skipping the Go toolchain for convenience — a real VPS wouldn't have it pre-built); everything else you do here is real:

```bash
# self-signed TLS cert, same as any first-time self-hosted setup
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout ~/tls.key -out ~/tls.crt -subj "/CN=localhost"

# No --workspace-dir/--secrets-dir here on purpose — the defaults already
# point at /var/lib/codeeditor/{workspaces,secrets}, which is what's
# bind-mounted above. A real self-hosted machine has no reason to ever
# override these, so neither should this stand-in for one.
bridged --port 8443 --tls-cert ~/tls.crt --tls-key ~/tls.key &
```

Leave that running (or open a second SSH session) and confirm it's actually serving:

```bash
curl -sk https://localhost:8443/handshake
```

You should see `bridge-daemon-reachable` and `container-engine` come back `verified` (Docker CLI here talks to the real host's daemon via the mounted socket) and `mosh` `verified` too (`mosh-server` is installed in the image). This is the real checklist `HostConnectionView` streams — nothing about this response is faked for the test.

## Point the app at it

In `HostConnectionView`, connect to `localhost:8443` (self-hosted). The onboarding checklist, clone, credential generation, and provisioning steps all run for real against this container — same verification already done manually with `curl` in `../../README.md` "Deployment", now reachable through the actual app UI instead of the command line.

## Cleaning up

```bash
docker compose down                                                              # stop the container; /var/lib/codeeditor on the real Docker host survives
colima ssh -- rm -rf /var/lib/codeeditor/workspaces/* /var/lib/codeeditor/secrets/*   # wipe cloned Workspaces/credentials too — no sudo needed, already chowned to you above
```

(On Docker Desktop instead of Colima, drop `colima ssh --` from that last line — the path is real on macOS directly there.)
