#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
list=$(mktemp "${TMPDIR:-/tmp}/files.XXXXXX") || exit 1
trap 'rm -f "$list"' EXIT
git -c core.quotePath=false ls-files -z --cached --others --exclude-standard -- "$@" >"$list" || exit 1
if [ $# = 0 ] && [ ! -s "$list" ]; then
	echo "scripts/files.sh: git lists no files" >&2
	exit 1
fi
xargs -0 sh -c 'for f do [ -f "$f" ] && printf "%s\0" "$f"; done; exit 0' sh <"$list"
