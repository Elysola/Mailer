#!/bin/sh
# Build one WASM plugin with TinyGo.
#
# Usage: scripts/build-plugin.sh plugins/otp
#
# The output is <dir>/<basename>.wasm, matching internal/plugin's default
# entrypoint convention (a manifest at x/plugin.yaml defaults to x.wasm).
#
# Plugins live in their own Go modules, so each is built with its own module
# context. TinyGo is required; set TINYGO to override the binary.

dir=${1:-}
name=
rc=0

usage() {
	echo "usage: build-plugin.sh <plugin-dir>" >&2
}

main() {
	if [ -z "$dir" ]; then
		usage
		return 2
	fi
	if [ ! -f "$dir/go.mod" ]; then
		echo "no go.mod in $dir" >&2
		return 1
	fi

	tinygo=${TINYGO:-tinygo}
	target=${TINYGO_TARGET:-wasip1}

	if ! command -v "$tinygo" >/dev/null 2>&1; then
		echo "tinygo not found; install it or set TINYGO=/path/to/tinygo" >&2
		return 1
	fi

	name=$(basename "$dir")
	echo "building $dir -> $dir/$name.wasm"

	cd "$dir" || return 1
	"$tinygo" build -target="$target" -buildmode=c-shared -no-debug -o "$name.wasm" .
}

main "$@"
rc=$?
exit $rc
