#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
tmp=$(mktemp -d "${TMPDIR:-/tmp}/no-tests.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT

sh scripts/files.sh >"$tmp/z" || exit 1
tr '\0' '\n' <"$tmp/z" >"$tmp/files"

matches() {
	[ -f "$1" ] || return 0
	grep -nE "$3" "$1" | cut -d: -f1 | while IFS= read -r n; do printf '%s:%s: %s\n' "$1" "$n" "$2"; done
}
go_import='^[[:space:]]*(import[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*[[:space:]]+|[._][[:space:]]+)?"(testing(/[^"]*)?|[^"]*/httptest|github\.com/stretchr/testify(/[^"]*)?)"[[:space:]]*(//.*)?$'
node_test='(from[[:space:]]*|import[[:space:]]*\(?[[:space:]]*|require[[:space:]]*\([[:space:]]*)["'\''](node:)?test["'\'']'
framework='"(jest|vitest|mocha|ava|tap|jasmine|karma|cypress|@playwright/test|@jest/[^"]*|@vitest/[^"]*)"[[:space:]]*:'

{
	grep -E '(^|/)[^/]*_test\.go$' "$tmp/files" | sed 's/$/: Go test file/'
	grep -E '(^|/)(testdata|__tests__)/' "$tmp/files" | sed 's/$/: in a test folder/'
	grep -E '(^|/)[^/]+\.(test|spec)\.[cm]?[jt]sx?$' "$tmp/files" | sed 's/$/: test file/'
	while IFS= read -r f; do
		case $f in
		*.go) matches "$f" "imports a Go testing package" "$go_import" ;;
		*.ts | *.tsx | *.mts | *.cts | *.js | *.mjs | *.cjs) matches "$f" "imports node:test" "$node_test" ;;
		package.json | */package.json) matches "$f" "names a test framework" "$framework" ;;
		esac
	done <"$tmp/files"
} >"$tmp/problems"

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	echo "$(wc -l <"$tmp/problems" | tr -d ' ') problem(s): the repository has no automated tests."
	exit 1
fi
echo "No tests or test tooling found."
