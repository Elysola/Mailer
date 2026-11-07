#!/bin/sh
# Build or clean every WASM plugin under plugins/.
#
# Usage: scripts/plugins.sh build
#        scripts/plugins.sh clean
#
# Iterating in a real script rather than in a Makefile recipe keeps the loop
# out of make's expansion rules: a compound shell statement written inline is
# parsed by make first, and lint tools then read the mangled result as shell.
# Here the loop is ordinary POSIX sh, which lint tools understand.
#
# Each plugin is its own Go module, so the work is delegated to
# build-plugin.sh per directory.

set -eu

action=${1:-}

usage() {
	echo "usage: plugins.sh {build|clean}" >&2
}

# Directories holding a plugin go.mod; empty output is fine (nothing to do).
plugin_dirs() {
	for mod in plugins/*/go.mod; do
		[ -e "$mod" ] || continue
		dirname "$mod"
	done
}

[ -n "$action" ] || { usage; exit 2; }

case $action in
build)
	plugin_dirs | while IFS= read -r dir; do
		./scripts/build-plugin.sh "$dir"
	done
	;;
clean)
	plugin_dirs | while IFS= read -r dir; do
		rm -f "$dir/$(basename "$dir").wasm"
		echo "removed $dir/$(basename "$dir").wasm"
	done
	;;
*)
	usage
	exit 2
	;;
esac
