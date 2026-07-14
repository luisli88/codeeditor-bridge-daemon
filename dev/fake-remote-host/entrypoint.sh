#!/bin/sh
# The host's docker.sock GID (Docker Desktop, most Linux hosts) almost
# never matches the container's local `docker` group GID picked at image
# build time — `developer` being *in* the `docker` group doesn't help if
# the group's GID itself doesn't match the socket's actual owning group.
# Confirmed hitting this on Docker Desktop for Mac: `docker info` failed
# with a permission error until the local group's GID was realigned to
# the mounted socket's real GID, at container start (it varies per host,
# so this can't be baked in at build time).
set -e

if [ -S /var/run/docker.sock ]; then
    sock_gid="$(stat -c '%g' /var/run/docker.sock)"
    if ! getent group "$sock_gid" > /dev/null 2>&1; then
        groupmod -g "$sock_gid" docker
    fi
fi

exec /usr/sbin/sshd -D -e
