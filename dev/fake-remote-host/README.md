# fake-remote-host

A bare, SSH-reachable container that stands in for a real self-hosted machine (VPS, Mac mini) — for testing the app's UF-001 connection flow (`HostConnectionView`) end to end, including the real `/handshake` checklist, instead of just running `../../Dockerfile` (the daemon's own image) with its port published straight to `localhost`.

**This does not auto-start the Bridge Daemon.** You SSH in and set it up yourself, the same steps a real self-hosted developer would follow (`../../README.md` "Self-hosted") — that's the point: it simulates the *machine*, not the daemon already running on it.

**What this container's SSH access is *not* used for**: the app's own connection flow. UF-001's `/handshake` check is a plain HTTPS request straight to the Bridge Daemon's TLS port — no SSH involved. This container's SSH server exists for you to set the daemon up over, and later for the Terminal/`mosh` feature (`MoshTerminalService.SSHCommandRunning` — currently only a test-only fake, see `../../README.md`).

## Start it

```bash
cd dev/fake-remote-host
docker compose up --build -d
```

This publishes `localhost:2222` → the container's SSH (port 22) and reserves `localhost:8443` for the Bridge Daemon once you start it (below). Two named volumes persist `/var/lib/codeeditor/{workspaces,secrets}` across container restarts.

## SSH in and set the daemon up

```bash
ssh developer@localhost -p 2222   # password: codeeditor
```

Inside the SSH session — this is exactly `../../README.md`'s "Self-hosted" flow, just against this fake machine instead of a real one. `bridged` is already built into the image at `/usr/local/bin/bridged` (skipping the Go toolchain for convenience — a real VPS wouldn't have it pre-built); everything else you do here is real:

```bash
# self-signed TLS cert, same as any first-time self-hosted setup
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout ~/tls.key -out ~/tls.crt -subj "/CN=localhost"

bridged --port 8443 --tls-cert ~/tls.crt --tls-key ~/tls.key \
  --workspace-dir /var/lib/codeeditor/workspaces \
  --secrets-dir /var/lib/codeeditor/secrets &
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
docker compose down           # stop, keep the named volumes (workspaces/secrets survive)
docker compose down -v        # stop and delete them too
```
