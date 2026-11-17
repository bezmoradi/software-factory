#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
max=$1
case $max in
'' | *[!0-9]*)
	echo "scripts/check-instructions.sh: give CLAUDE.md's byte budget as a whole number, as make instructions does"
	exit 1
	;;
esac
tmp=$(mktemp -d "${TMPDIR:-/tmp}/instructions.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT
root=CLAUDE.md
rules=.claude/rules
: >"$tmp/problems"
problem() {
	printf '%s: %s\n' "$1" "$2" >>"$tmp/problems"
}

for f in * .[!.]*; do
	[ -e "$f" ] || continue
	[ "$(printf '%s' "$f" | tr '[:upper:]' '[:lower:]')" = agents.md ] &&
		problem "$f" "remove it: the rules are in CLAUDE.md and $rules/, and a second file would split them for other tools"
done

count=0
if [ -f "$root" ]; then
	size=$(wc -c <"$root" | tr -d ' ')
	[ "$size" -le "$max" ] || problem "$root" "$size bytes, over its budget of $max (INSTRUCTIONS_MAX_BYTES in the Makefile); move scoped rules to $rules/"
	grep -oE '\]\(\.claude/rules/[^)#[:space:]]*' "$root" | sed 's/^](//' | LC_ALL=C sort -u >"$tmp/linked"
	while IFS= read -r l; do
		[ -f "$l" ] || problem "$root" "links $l, which doesn't exist"
	done <"$tmp/linked"
else
	problem "$root" "is missing"
	: >"$tmp/linked"
fi

for f in "$rules"/*.md; do
	[ -f "$f" ] || continue
	count=$((count + 1))
	git check-ignore -q -- "$f" && problem "$f" "git ignores it, so it would never be committed"
	grep -qxF "$f" "$tmp/linked" || problem "$f" "$root doesn't link it; add a row to the table in \"How the instructions are organized\""
	awk -v f="$f" -v tmp="$tmp" '
		function bad(m) { printf "%s: %s\n", f, m > (tmp "/problems") }
		NR == 1 { if ($0 != "---") { bad("doesn'\''t start with a --- frontmatter line"); exit } ; next }
		$0 == "---" { closed = 1; exit }
		/^paths:[[:space:]]*$/ { inpaths = 1; seen = 1; next }
		/^paths:/ { bad("write paths: as a list, one \"glob\" per line under it"); seen = 1; next }
		inpaths && /^[[:space:]]+-/ {
			if ($0 ~ /^[[:space:]]+- "[^"]+"[[:space:]]*$/) { g = $0; sub(/^[[:space:]]+- "/, "", g); sub(/"[[:space:]]*$/, "", g); print g > (tmp "/globs"); items++ }
			else bad("a paths: entry that isn'\''t one double-quoted glob: " $0)
			next
		}
		{ inpaths = 0 }
		END {
			if (NR > 1 && !closed) bad("its frontmatter has no closing --- line")
			else if (closed && !seen) bad("its frontmatter has no paths: list, so Claude Code would load it at every start")
			else if (seen && items == 0) bad("its paths: list has no globs")
		}' "$f"
	if [ -f "$tmp/globs" ]; then
		while IFS= read -r g; do
			[ -n "$(git ls-files --cached --others --exclude-standard -- ":(glob)$g" | head -n 1)" ] ||
				problem "$f" "the glob \"$g\" matches no file in the repository, so the file would never load"
		done <"$tmp/globs"
		rm -f "$tmp/globs"
	fi
done
[ "$count" -gt 0 ] || problem "$rules" "holds no rule files"

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	echo "$(wc -l <"$tmp/problems" | tr -d ' ') problem(s) in the instruction files."
	exit 1
fi
echo "Instructions: $count rule files in $rules/, each with a paths: list of quoted globs that match files and linked from $root, which is $size of $max bytes; no AGENTS.md."
