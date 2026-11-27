#!/bin/sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=${PROCTOR_SF_CHECKOUT:-$(cd "$here/../.." && pwd)}
pagedir=$repo/internal/adapters/http/page
dims=$repo/proctor/dimensions.yaml
config=$repo/proctor/proctor.yaml
min_routes=80

say() { printf 'sf-proctor routes: %s\n' "$*" >&2; }

work=$(mktemp -d "${TMPDIR:-/tmp}/sf-proctor-routes.XXXXXX")
trap 'rm -rf "$work"' EXIT

sed -n 's/^[[:space:]]*\(const[[:space:]][[:space:]]*\)\{0,1\}\([A-Za-z][A-Za-z0-9]*Route\)[[:space:]]*=[[:space:]]*"\(\/api\/[^"]*\)".*$/\2 \3/p' \
	"$pagedir"/*.go >"$work/consts"

names() {
	tr ',' '\n' | sed -n 's/^[[:space:]]*\([^:[:space:]][^:[:space:]]*\)[[:space:]]*:[[:space:]]*true.*$/\1/p'
}
{
	sed -n '/^var pageRoutes = map\[string\]bool{$/,/^}$/p' "$pagedir/page.go" | sed '1s/^.*{//' | names
	sed -n 's/^[[:space:]]*workflowRoutes[[:space:]]*=[[:space:]]*map\[string\]bool{\(.*\)}[[:space:]]*$/\1/p' "$pagedir/workflow.go" | names
	echo signinRoute
} >"$work/names"

: >"$work/code"
while read -r name; do
	case "$name" in
		\"/api/*\")
			name=${name#\"}
			echo "${name%\"}" >>"$work/code"
			;;
		*)
			route=$(sed -n "s/^$name \(.*\)$/\1/p" "$work/consts")
			[ -n "$route" ] || {
				say "can't find the path of $name in $pagedir"
				exit 1
			}
			echo "$route" >>"$work/code"
			;;
	esac
done <"$work/names"
sort -u "$work/code" -o "$work/code"
count=$(wc -l <"$work/code" | tr -d ' ')
[ "$count" -ge "$min_routes" ] || {
	say "found only $count page routes in the code, fewer than $min_routes; the parser is probably broken"
	exit 1
}
grep -vx /api/events "$work/code" >"$work/post"

sed -n '/^  post_routes:$/,/^ \{0,2\}[^ ]/p' "$dims" | sed '1d;$d' >"$work/dim-block"
sed -n 's/^    - "\(\/api\/[^"]*\)"$/\1/p' "$work/dim-block" >"$work/dim"
[ -s "$work/dim" ] || {
	say "found no post_routes in proctor/dimensions.yaml; the parser is probably broken"
	exit 1
}
[ "$(grep -c . "$work/dim-block")" -eq "$(wc -l <"$work/dim" | tr -d ' ')" ] || {
	say "proctor/dimensions.yaml has a post_routes entry this script can't read; write each on its own line as - \"/api/<route>\""
	exit 1
}

failed=0
if [ -n "$(sort "$work/dim" | uniq -d)" ]; then
	say "post_routes in proctor/dimensions.yaml lists a route twice: $(sort "$work/dim" | uniq -d | tr '\n' ' ')"
	failed=1
fi
sort -u "$work/dim" -o "$work/dim"
for r in $(comm -23 "$work/post" "$work/dim"); do
	say "post_routes in proctor/dimensions.yaml doesn't list $r, which the page serves"
	failed=1
done
for r in $(comm -13 "$work/post" "$work/dim"); do
	say "post_routes in proctor/dimensions.yaml lists $r, which the page doesn't serve as a POST route"
	failed=1
done

policy_list() {
	sed -n "/^      $1:\$/,/^ \{0,6\}[^ ]/p" "$config"
}
policy_list reads >"$work/reads-block"
policy_list destructive >"$work/destructive-block"
sed -n 's/^        - { method: POST, path: \([^ ,}]*\).*$/\1/p' "$work/reads-block" | sort -u >"$work/reads"
[ -s "$work/reads" ] || {
	say "found no declared reads in proctor/proctor.yaml; the parser is probably broken"
	exit 1
}
sed -n 's/^        - { method: POST, path: \([^ ,}]*\) }$/\1/p' "$work/destructive-block" | sort -u >"$work/destructive"
[ "$(grep -c '^        - ' "$work/destructive-block")" -eq "$(wc -l <"$work/destructive" | tr -d ' ')" ] || {
	say "proctor/proctor.yaml has a destructive entry this script can't read; write each as { method: POST, path: <route> }"
	exit 1
}
sed -n 's/^        - { method: POST, path: [^ ,}]*, shows: \[\([^]]*\)\] }$/\1/p' "$work/reads-block" >"$work/shows"
sed -n '/^relations:$/,/^[^ ]/p' "$config" >"$work/relations-block"
sed -n 's/^  - act: { driver: http, target: \([^ ,}]*\) }$/\1/p' "$work/relations-block" >"$work/related-all"
[ "$(grep -c '^  - act:' "$work/relations-block")" -eq "$(wc -l <"$work/related-all" | tr -d ' ')" ] || {
	say "proctor/proctor.yaml has a relation this script can't read; write each act as { driver: http, target: <route> }"
	exit 1
}
sort -u "$work/related-all" >"$work/related"
[ "$(sed '/^relations:$/,/^[^ ]/d' "$config" | grep -c 'shows:')" -eq "$(wc -l <"$work/shows" | tr -d ' ')" ] || {
	say "proctor/proctor.yaml has a shows list this script can't read; write each on its reads entry's line, as { method: POST, path: <route>, shows: [<route>, ...] }"
	exit 1
}
tr ',' '\n' <"$work/shows" | tr -d ' ' | sort -u >"$work/shown"
for r in $(comm -13 "$work/code" "$work/reads"); do
	say "proctor/proctor.yaml declares POST $r a read, but the page serves no such route"
	failed=1
done
for r in $(comm -13 "$work/code" "$work/destructive"); do
	say "proctor/proctor.yaml declares POST $r destructive, but the page serves no such route"
	failed=1
done
for r in $(comm -13 "$work/code" "$work/related"); do
	say "proctor/proctor.yaml relates an act on POST $r, but the page serves no such route"
	failed=1
done
for r in $(comm -13 "$work/code" "$work/shown"); do
	say "proctor/proctor.yaml says a read shows $r, but the page serves no such route"
	failed=1
done

if [ "$failed" -ne 0 ]; then
	say "proctor/dimensions.yaml or proctor/proctor.yaml doesn't match the page's $count routes; update them (.claude/rules/proctor.md)"
	exit 1
fi
say "post_routes in proctor/dimensions.yaml lists every one of the page's $count routes but /api/events, and every declared path is one the page serves; proctor validate checks the tables against it"
