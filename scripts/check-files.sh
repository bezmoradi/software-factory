#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
limit=524288
tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-files.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT

sh scripts/files.sh >"$tmp/z" || exit 1
tr '\0' '\n' <"$tmp/z" >"$tmp/list"

while IFS= read -r f; do
	[ -f "$f" ] || continue
	size=$(wc -c <"$f" | tr -d ' ')
	[ "$size" -gt "$limit" ] && printf '%s: %s bytes, over 512 KiB\n' "$f" "$size"
	grep -Iq . "$f" 2>/dev/null || continue
	grep -Il "$(printf '\r')" "$f" >/dev/null 2>&1 && printf '%s: CRLF line endings\n' "$f"
	grep -InE '^(<<<<<<< |>>>>>>> |=======$)' "$f" 2>/dev/null | sed "s|^|$f:|; s|$|: a merge conflict marker|"
done <"$tmp/list" >"$tmp/problems"

tr '[:upper:]' '[:lower:]' <"$tmp/list" | sort | uniq -d | sed 's|$|: two paths differ only by case|' >>"$tmp/problems"

grep -nE '<script>|<script [^>]*>[^<]|<style|[[:space:]]style=|[[:space:]]on[a-z]+=' web/index.html | sed 's|^|web/index.html:|; s|$|: inline script, style or handler, which the page'"'"'s CSP refuses|' >>"$tmp/problems"

grep -E '\.(go|ts)$' "$tmp/list" | grep -v '^dev/' >"$tmp/code"
while IFS= read -r f; do
	grep -nE 'credentials\.json|find-generic-password|[Kk]eychain|setup-token|oauth/token' "$f" | sed "s|^|$f:|; s|$|: code that names a credential store, which no code reads|"
	grep -nE 'Getenv\("(ANTHROPIC_API_KEY|ANTHROPIC_AUTH_TOKEN|CLAUDE_CODE_OAUTH_TOKEN|OPENAI_API_KEY|CODEX_API_KEY)"' "$f" | sed "s|^|$f:|; s|$|: code that reads a model key variable|"
	grep -nE 'exec\.Command(Context)?\((ctx, )?"git"' "$f" | sed "s|^|$f:|; s|$|: code that runs git, which runs the repository's own configuration|"
	case "$f" in
	web/*.ts | plugin/*.ts) grep -nE 'innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function\(' "$f" | sed "s|^|$f:|; s|$|: HTML or code built from strings|" ;;
	esac
done <"$tmp/code" >>"$tmp/problems"

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	exit 1
fi
echo "Files: none over 512 KiB, no CRLF, no conflict markers, no paths differing only by case, no inline script or style in the page, no code naming a credential store, reading a model key or running git, and no HTML or code built from strings."
