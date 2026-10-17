#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export GOCACHE="${GOCACHE:-$root/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$root/.cache/go-mod}"
export GOTOOLCHAIN="go$(cat "$root/.go-version")"
exec go "$@"
