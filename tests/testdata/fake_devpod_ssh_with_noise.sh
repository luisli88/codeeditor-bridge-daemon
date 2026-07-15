#!/bin/sh
# Same as fake_devpod_ssh.sh, plus a real devpod ssh's own tunnel-teardown
# line on stderr after the command finishes — for asserting that noise
# gets filtered rather than relayed to the Desarrollador as if it were
# the program's own stderr. See
# tests/devpodexec_fake_test.go's fakeDevpodSSHScriptWithDevpodNoise.
if [ "$1" = "ssh" ]; then
  shift 2
  if [ "$1" = "--command" ]; then
    sh -c "$2" # not exec'd — needs to return so the noise line below still runs
    echo "Error tunneling to container: wait: remote command exited without exit status or exit signal" 1>&2
    exit 0
  fi
fi
exit 1
