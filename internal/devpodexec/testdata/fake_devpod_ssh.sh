#!/bin/sh
# Stands in for the real `devpod` CLI's `ssh <workspaceID> --command <cmd>`
# invocation — re-execs <cmd> locally via `sh -c`, inheriting
# stdin/stdout/stderr, so the real framing/streaming code on the Go side
# still runs against a real subprocess. See exec_test.go's
# fakeDevpodSSHScript.
if [ "$1" = "ssh" ]; then
  shift 2 # drop "ssh" <workspaceID>
  if [ "$1" = "--command" ]; then
    exec sh -c "$2"
  fi
fi
exit 1
