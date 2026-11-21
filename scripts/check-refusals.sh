#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-refusals.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT

scan='
function opened(s,   n, i, c) {
	n = 0
	for (i = 1; i <= length(s); i++) {
		c = substr(s, i, 1)
		if (c == "{") n++
		if (c == "}") n--
	}
	return n
}
FNR == 1 { fn = ""; held = ""; depth = 0 }
/^func / {
	fn = $0
	sub(/^func (\([^)]*\) )?/, "", fn)
	sub(/\(.*/, "", fn)
}
{
	agentSide = (fn in allowed)
	if (held != "") {
		held = held " " $0
		depth += opened($0)
		if (depth <= 0) {
			if (held !~ /Message:/) printf "%s:%d: %s\n", FILENAME, heldAt, what
			held = ""
		}
	} else if (!agentSide && $0 ~ literal) {
		rest = substr($0, match($0, literal))
		depth = opened(rest)
		if (depth <= 0) {
			if (rest !~ /Message:/) printf "%s:%d: %s\n", FILENAME, FNR, what
		} else {
			held = rest
			heldAt = FNR
		}
	}
	if (!agentSide && wordless != "" && $0 ~ wordless) printf "%s:%d: %s\n", FILENAME, FNR, "a refusal the agent listener sends, outside its handlers; a page answer needs a message in words"
	if (empty != "" && $0 ~ empty) printf "%s:%d: %s\n", FILENAME, FNR, "a refusal whose message is empty"
	if (maps && !agentSide && $0 ~ /"error":/ && $0 !~ /"message":/) printf "%s:%d: %s\n", FILENAME, FNR, "an error answer without a \"message\" in words"
}
'

core_allowed="Register Poll Report Wait Wake FromBox PushSession checking Image"
page_allowed="agent register agentReject agentRefuse agentError busy pushRequest check forgeRequest checksRequest commentsRequest splitRequest forgeSession turnCheck sessionModel image"

run() {
	allowed=$1
	literal=$2
	wordless=$3
	empty=$4
	maps=$5
	what=$6
	shift 6
	awk -v list="$allowed" -v literal="$literal" -v wordless="$wordless" -v empty="$empty" -v maps="$maps" -v what="$what" \
		"BEGIN { n = split(list, a, \" \"); for (i = 1; i <= n; i++) allowed[a[i]] = 1 } $scan" "$@"
}

(
	set --
	for f in internal/core/session/*.go; do
		[ "$f" = internal/core/session/errors.go ] || set -- "$@" "$f"
	done
	run "$core_allowed" '(&|[^A-Za-z.])Error[{]' '(toAgent|unauthorized)[(]' '(invalid|conflict|notFound|limited|full)[(]"[^"]*", ""[)]|explained[(][^,]+, ""[)]' 0 "a session.Error without a Message in words" "$@"
) >"$tmp/problems"
find internal/adapters cmd -name '*.go' ! -path 'internal/adapters/http/page/*' >"$tmp/outside"
(
	set --
	while IFS= read -r f; do
		set -- "$@" "$f"
	done <"$tmp/outside"
	[ "$#" -gt 0 ] || { echo "scripts/check-refusals.sh: found no Go file in internal/adapters or cmd" >&2; exit 1; }
	run "" 'session[.]Error[{]' '' '' 0 "a session.Error without a Message in words" "$@"
) >>"$tmp/problems" || exit 1
run "$page_allowed" '(problem|capsProblem)[{]' '(agentRefuse|agentError)[(]' '(refuse|reject|refuseCaps|refuseDepth)[(].*, ""[)]' 1 "an error answer without a Message in words" \
	internal/adapters/http/page/*.go >>"$tmp/problems"

fieldMax=60
long='
function runes(s) {
	gsub(/[\200-\277]/, "", s)
	return length(s)
}
function check(line, pat,   m, n, parts) {
	while (match(line, pat)) {
		m = substr(line, RSTART, RLENGTH)
		line = substr(line, RSTART + RLENGTH)
		n = split(m, parts, "\"")
		if (runes(parts[n - 1]) > max) printf "%s:%d: a field message over %d characters (%d): %s\n", FILENAME, FNR, max, runes(parts[n - 1]), parts[n - 1]
	}
}
function summed(line, pat,   rest, i, c, depth, quoted, lit, total, shown) {
	while (match(line, pat)) {
		rest = substr(line, RSTART + RLENGTH)
		line = rest
		depth = 1
		quoted = 0
		lit = ""
		total = 0
		shown = ""
		for (i = 1; i <= length(rest) && depth > 0; i++) {
			c = substr(rest, i, 1)
			if (quoted) {
				if (c == "\\") {
					lit = lit substr(rest, i + 1, 1)
					i++
				} else if (c == "\"") {
					quoted = 0
					total += runes(lit)
					shown = shown (shown == "" ? "" : " + ") lit
					lit = ""
				} else lit = lit c
			} else if (c == "\"") quoted = 1
			else if (c == "(") depth++
			else if (c == ")") depth--
		}
		if (total > max) printf "%s:%d: a field message whose words alone are over %d characters (%d): %s\n", FILENAME, FNR, max, total, shown
	}
}
{
	if (FILENAME ~ /^internal\/core\//) {
		if ($0 ~ /onField[(]/) summed($0, "(invalid|conflict|notFound|limited|full)[(]\"[^\"]*\", ")
		else summed($0, "(invalid|conflict|notFound|limited|full)[(]\"(from|message|issue|benchmark)\", ")
		summed($0, "(^|[^A-Za-z_.])(refusal|bad|withName)[(]")
	}
	if (FILENAME ~ /^internal\/adapters\/http\/page\//) {
		check($0, "Field: [^,}]+, Message: \"[^\"]*\"")
		check($0, "refuse(Caps|Depth)[(]w, [^,]+, \"[^\"]*\"")
	}
	check($0, "[A-Za-z]+Rule +=  *\"[^\"]*\"")
	check($0, "Rejected: true, Problem: \"[^\"]*\"")
}
'
find internal/core internal/adapters -name '*.go' ! -path '*/dist/*' -exec env LC_ALL=C awk -v max="$fieldMax" "$long" {} + >>"$tmp/problems"

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	exit 1
fi
echo "Refusals: every refusal the page listener sends, and every session.Error but the agent listener's, carries a message in words, and no field message is over $fieldMax characters."
