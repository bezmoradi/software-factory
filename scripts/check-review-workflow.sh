#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
wf=.github/workflows/claude-code-review.yml
tmp=$(mktemp -d "${TMPDIR:-/tmp}/review-workflow.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT
failed=0
problem() {
	printf '%s: %s\n' "$wf" "$1"
	failed=1
}

[ -f "$wf" ] || {
	problem "is missing"
	exit 1
}
grep -v '^[[:space:]]*#' "$wf" >"$tmp/wf"

top=$(grep -E '^permissions:' "$tmp/wf" | sed 's/[[:space:]]*#.*//; s/[[:space:]]*$//' | tr '\n' '|')
[ "$top" = 'permissions: {}|' ] || problem "the workflow must set \`permissions: {}\` once at the top, not: ${top:-nothing}"

awk '/^[^[:space:]]/ { injobs = ($0 ~ /^jobs:/); job = ""; next }
	injobs && /^  [^[:space:]]/ { job = $0; sub(/^  /, "", job); sub(/:.*/, "", job); next }
	injobs && job == "review"' "$tmp/wf" >"$tmp/review"
[ -s "$tmp/review" ] || problem "has no review job"

[ "$(grep -c '^    permissions:' "$tmp/review")" = 1 ] || problem "the review job must have one permissions key"
perms=$(awk '
	function indent(s) { match(s, /^ */); return RLENGTH }
	/^    permissions:/ { on = 1; v = $0; sub(/^    permissions:[[:space:]]*/, "", v); sub(/[[:space:]]*#.*/, "", v); if (v != "") print v; next }
	on && NF && indent($0) <= 4 { on = 0 }
	on && NF { v = $0; sub(/[[:space:]]+#.*/, "", v); gsub(/[[:space:]]/, "", v); print v }' "$tmp/review" | LC_ALL=C sort | tr '\n' ' ')
[ "$perms" = 'contents:read pull-requests:write ' ] ||
	problem "the review job's permissions must be exactly contents: read and pull-requests: write, one per line, not: ${perms:-none}"

grep -Eq '^[[:space:]]+github_token:[[:space:]]*[$][{][{][[:space:]]*github[.]token[[:space:]]*[}][}][[:space:]]*$' "$tmp/review" ||
	problem "the review must pass github_token: \${{ github.token }}, or the action may use the Claude app's token, which can push"

flags=$(grep -oiE -e '--?(allowed|disallowed)[-_]?tools' "$tmp/wf" | LC_ALL=C sort | tr '\n' ' ')
[ "$flags" = '--allowedTools --disallowedTools ' ] ||
	problem "set the tools once each, as --allowedTools \"...\" and --disallowedTools \"...\", not: ${flags:-none}"
tool_list() {
	sed -n "s/.*--$1[[:space:]]\{1,\}\"\([^\"]*\)\".*/\1/p" "$tmp/review" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//; /^$/d'
}
for flag in allowedTools disallowedTools; do
	grep -e "--$flag" "$tmp/review" | grep -qv -e "--${flag}[[:space:]]\{1,\}\"" &&
		problem "write --$flag as one double-quoted, comma-separated list, which this check can read"
done
denied=$(tool_list disallowedTools)
missing=
for t in Edit Write MultiEdit NotebookEdit; do
	printf '%s\n' "$denied" | grep -qx "$t" || missing="$missing $t"
done
[ -z "$missing" ] || problem "--disallowedTools doesn't deny:$missing"
pr='${{ github.event.pull_request.number }}'
allowed=$(tool_list allowedTools)
[ -n "$allowed" ] || problem "--allowedTools lists nothing this check can read"
extra=$(printf '%s\n' "$allowed" | grep -vxF -e "Bash(gh pr diff $pr)" -e "Bash(gh pr diff $pr --name-only)" -e "Bash(gh pr view $pr)" \
	-e mcp__github_inline_comment__create_inline_comment | tr '\n' ' ')
[ -z "$extra" ] || problem "--allowedTools approves more than the three gh pr commands and the inline-comment tool: $extra"

bad=$(grep -oiE -e 'use_commit_signing|ssh_signing_key|id-token|write-all|mcp__github[_[:alnum:]]*' \
	-e 'dangerously-skip-permissions|skip[-_]?permissions|permission[-_]?mode|bypassPermissions|mcp[-_]?config|additional_permissions' \
	-e '--(settings|plugin-dir|add-dir)([^_[:alnum:]-]|$)' \
	-e '^[[:space:]]*(settings|plugins|plugin_marketplaces|allowed_non_write_users):' "$tmp/wf" |
	sed 's/^[[:space:]]*//; s/[^_[:alnum:]-]$//' | grep -vx mcp__github_inline_comment__create_inline_comment |
	LC_ALL=C sort -u | tr '\n' ' ')
[ -z "$bad" ] || problem "gives Claude a way to commit, push or merge, or to do more than review: $bad"

if [ "$failed" = 1 ]; then
	echo "Claude's review must only review."
	exit 1
fi
echo "Claude's review only reviews: its permissions, token, tools and options."
