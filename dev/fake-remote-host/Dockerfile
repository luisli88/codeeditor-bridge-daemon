# Simulates a bare self-hosted machine (VPS/Mac mini) for testing UF-001
# (HostConnectionView) end to end against something SSH-reachable, instead
# of just running ../../Dockerfile (the daemon's own image) with its port
# published straight to localhost. This is the "empty machine" a real
# self-hosted developer would SSH into and set the daemon up on by hand —
# it does NOT auto-start `bridged`; you do that yourself over SSH, the
# same steps README.md's "Self-hosted" section describes for a real VPS.
#
# What the app's own connection flow actually uses this container's SSH
# for: nothing yet. UF-001's /handshake check is a plain HTTPS GET on the
# daemon's TLS port — SSH here is for *you* to set the daemon up, and for
# the Terminal/mosh feature later (MoshTerminalService.SSHCommandRunning,
# currently only a test-only fake — see bridge-daemon/README.md).
#
# Build context MUST be the repo root (bridge-daemon/), not this
# directory — this Dockerfile needs the real source tree to build
# `bridged`. See docker-compose.yml, or build directly with:
#   docker build -f dev/fake-remote-host/Dockerfile -t codeeditor-fake-remote-host .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bridged ./cmd/bridged

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        openssh-server \
        ca-certificates \
        tmux \
        git \
        mosh \
        docker.io \
        sudo \
        wget \
        curl \
    && rm -rf /var/lib/apt/lists/* \
    && wget -qO /usr/local/bin/devpod \
        "https://github.com/loft-sh/devpod/releases/latest/download/devpod-linux-$(case "$(uname -m)" in \
            x86_64) echo amd64 ;; \
            aarch64) echo arm64 ;; \
        esac)" \
    && chmod +x /usr/local/bin/devpod

# A real self-hosted developer would SSH in as themselves, not root —
# password auth for local testing convenience; use a key for anything
# beyond your own machine.
RUN useradd -m -s /bin/bash -G docker,sudo developer \
    && echo "developer:codeeditor" | chpasswd \
    && echo "developer ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers \
    && mkdir -p /run/sshd \
    && ssh-keygen -A

COPY --from=build /out/bridged /usr/local/bin/bridged
COPY dev/fake-remote-host/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 22 8443
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
