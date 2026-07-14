# Bridge Daemon — persistent control plane (constitution Principio VII).
# This image is the "empty" container that runs the `bridged` binary
# itself; it is NOT where Workspace containers live. Workspaces are
# created on demand by `devpod up` against whatever `devcontainer.json`
# the target project has (or a generated default) — there is no fixed
# CodeEditor "workspace" image to build or register, that's the point of
# using DevPod (see bridge-daemon/README.md "Despliegue").
#
# This container needs a container runtime to hand to DevPod — it does
# NOT run Docker-in-Docker; it talks to the *host's* Docker via the
# mounted socket (see the `docker run -v /var/run/docker.sock:...`
# example in README.md). Workspace containers DevPod creates are then
# siblings of this container, not children of it.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bridged ./cmd/bridged

FROM alpine:3.20
RUN apk add --no-cache \
        ca-certificates \
        tmux \
        git \
        openssh-client \
        docker-cli \
    # DevPod CLI (research.md — no stable Go SDK, the daemon shells out
    # to the real binary via internal/devpod.SubprocessRunner).
    && wget -qO /usr/local/bin/devpod \
        "https://github.com/loft-sh/devpod/releases/latest/download/devpod-linux-$(case "$(uname -m)" in \
            x86_64) echo amd64 ;; \
            aarch64) echo arm64 ;; \
        esac)" \
    && chmod +x /usr/local/bin/devpod

COPY --from=build /out/bridged /usr/local/bin/bridged
# Pre-create the mount point — without it, bind-mounting a single file to
# a path whose parent doesn't exist yet in the image makes some Docker
# setups create a *directory* there instead of the file (confirmed on
# Docker Desktop for Mac), which then fails at read time, not mount time.
RUN mkdir -p /etc/bridged

EXPOSE 8443
ENTRYPOINT ["bridged"]
CMD ["--port", "8443", "--tls-cert", "/etc/bridged/tls.crt", "--tls-key", "/etc/bridged/tls.key"]
