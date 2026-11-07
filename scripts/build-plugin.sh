#!/bin/sh
# Build one WASM plugin with TinyGo.
#
# Usage: scripts/build-plugin.sh plugins/otp
#
# The output is <dir>/<basename>.wasm, which matches internal/plugin's default
# entrypoint convention (a manifest at x/plugin.yaml defaults to x.wasm).
#
# Plugins live in their own Go modules, so each is built with its own module
# context. TinyGo is required; set TINYGO to override the binary.
set -eu

dir=${1:-}
if [ -z "$dir" ]; then
	echo "usage: build-plugin.sh <plugin-dir>" >&2
	exit 2
fi
if [ ! -f "$dir/go.mod" ]; then
	echo "no go.mod in $dir" >&2
	exit 1
fi

tinygo=${TINYGO:-tinygo}
target=${TINYGO_TARGET:-wasip1}

if ! command -v "$tinygo" >/dev/null 2>&1; then
	echo "tinygo not found; install it or set TINYGO=/path/to/tinygo" >&2
	exit 1
fi

name=$(basename "$dir")
echo "building $dir -> $dir/$name.wasm"

cd "$dir"
"$tinygo" build -target="$target" -buildmode=c-shared -no-debug -o "$name.wasm" .
