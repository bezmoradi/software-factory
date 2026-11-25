#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
tmp=$(mktemp -d "${TMPDIR:-/tmp}/web-forms.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT

css=web/styles/app.css
first='^\.form \{'
last='^:is\(\.form-inline, \.form-row\) > \.form-actions \{'

for f in web/*.ts web/components/*.ts web/lib/*.ts; do
	[ "$f" = web/lib/form.ts ] && continue
	grep -nE "(\bel|createElement)\([[:space:]]*['\"\`](form|label)['\"\`]" "$f" | cut -d: -f1 | while IFS= read -r n; do
		printf '%s:%s: creates a <form> or <label>; build it with form() or field() from web/lib/form.ts\n' "$f" "$n"
	done
done >"$tmp/problems"

for f in web/*.ts web/components/*.ts web/lib/*.ts; do
	[ "$f" = web/lib/controls.ts ] && continue
	grep -nE "(\bel|createElement)\([[:space:]]*['\"\`](input|select|textarea|option|optgroup)['\"\`]" "$f" | cut -d: -f1 | while IFS= read -r n; do
		printf '%s:%s: creates a form control by hand; build it with textbox(), textarea(), dropdown(), option(), checkbox(), radio() or filePicker() from web/lib/controls.ts\n' "$f" "$n"
	done
done >>"$tmp/problems"

for f in web/*.ts web/components/*.ts web/lib/*.ts; do
	[ "$f" = web/api.ts ] && continue
	grep -nE '(^|[^.#[:alnum:]_$])fetch[[:space:]]*\(' "$f" | while IFS=: read -r n line; do
		[ "$f" = web/stream.ts ] && printf '%s\n' "$line" | grep -qE '(^|[^.#[:alnum:]_$])fetch\(import\.meta\.url,' && continue
		printf '%s:%s: calls fetch; send it with request() from web/api.ts\n' "$f" "$n"
	done
	grep -nE '\.json[[:space:]]*\(' "$f" | cut -d: -f1 | while IFS= read -r n; do
		printf '%s:%s: reads JSON itself; take the answer from request() in web/api.ts\n' "$f" "$n"
	done
done >>"$tmp/problems"

for f in web/*.ts web/components/*.ts web/lib/*.ts web/index.html; do
	[ "$f" = web/lib/page.ts ] && continue
	grep -nE '(page-head|page-intro|docs-link|docs-open|empty-state)([^[:alnum:]_-]|$)|View in docs' "$f" | cut -d: -f1 | while IFS= read -r n; do
		printf '%s:%s: builds a heading row, its intro, a View in docs link, an Open button in the docs or an empty state by hand; use pageHead(), docsLink(), dialogLink() or emptyState() from web/lib/page.ts\n' "$f" "$n"
	done
done >>"$tmp/problems"

q="'"
either="([Mm]essage|said)\\)?[[:space:]]*(\\|\\||\\?\\?)[[:space:]]*"
fallback="(^|[^!])${either}[$q\"\\\`A-Z(]"
empty="$either($q$q|\"\")([^$q\"]|\$)"
ternary="([Mm]essage|said)[[:space:]]*\\?[^.?:][^:]*:[[:space:]]*[$q\"\\\`][^$q\"\\\`]"
for f in web/*.ts web/components/*.ts web/lib/*.ts; do
	[ "$f" = web/api.ts ] && continue
	{
		grep -nE "$fallback" "$f" | grep -vE "$empty"
		grep -nE "$ternary" "$f"
		grep -nE "(showAnswer\\(.*|[{,][[:space:]]*)fallback[[:space:]]*:[[:space:]]*[$q\"\\\`A-Z]" "$f"
	} | cut -d: -f1 | sort -un | while IFS= read -r n; do
		printf '%s:%s: falls back to words of its own when an engine answer has no message; show the answer'"'"'s message, which web/api.ts always fills for a refusal\n' "$f" "$n"
	done
done >>"$tmp/problems"

grep -niE '<(form|label)([[:space:]>/]|$)' web/index.html | cut -d: -f1 | while IFS= read -r n; do
	printf 'web/index.html:%s: a <form> or <label> in the page; build it with form() or field() from web/lib/form.ts\n' "$n"
done >>"$tmp/problems"

start=$(grep -nE "$first" "$css" | head -n 1 | cut -d: -f1)
end=$(grep -nE "$last" "$css" | head -n 1 | cut -d: -f1)
if [ -z "$start" ] || [ -z "$end" ] || [ "$start" -gt "$end" ]; then
	printf '%s: the form rules (from ".form {" to ":is(.form-inline, .form-row) > .form-actions {") are missing or out of order\n' "$css" >>"$tmp/problems"
else
	awk -v start="$start" -v end="$end" -v file="$css" '
		NR >= start && NR <= end { next }
		/^[[:space:]]*@/ { next }
		index($0, "{") == 0 { next }
		{
			sel = substr($0, 1, index($0, "{") - 1)
			body = substr($0, index($0, "{") + 1)
			if (body !~ /(^|[ ;])(gap|row-gap|column-gap|margin|margin-[a-z-]+|padding|padding-[a-z-]+)[[:space:]]*:/) next
			n = split(sel, parts, ",")
			for (i = 1; i <= n; i++) {
				s = parts[i]
				gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
				gsub(/:(not|is|where|has)\([^)]*\)/, "", s)
				m = split(s, words, /[[:space:]]+|[[:space:]]*[>+~][[:space:]]*/)
				subject = words[m]
				if (subject ~ /^(form|label)([.:[]|$)/ || subject ~ /\.(form|form-inline|form-row|form-actions|field|field-inline|field-check|field-option|field-hidden)([.:[]|$)/) {
					printf "%s:%d: spacing on a form, field, label or actions row outside the form rules; change the form rules instead\n", file, NR
					next
				}
			}
		}
	' "$css" >>"$tmp/problems"
fi

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	echo "$(wc -l <"$tmp/problems" | tr -d ' ') problem(s): every form, label and its spacing comes from web/lib/form.ts and the form rules in $css, every form control from web/lib/controls.ts, and every heading row, View in docs link, Open button in the docs and empty state from web/lib/page.ts, and every request to the engine and reading of its answer from web/api.ts, and no component falls back to words of its own for an engine answer's message."
	exit 1
fi
echo "Forms: only web/lib/form.ts creates a <form> or <label>, only web/lib/controls.ts creates an input, select, textarea, option or optgroup, and only the form rules in $css space forms, fields, labels and actions rows. Heading rows, View in docs links, Open buttons in the docs and empty states: only web/lib/page.ts builds them. Requests: only web/api.ts calls fetch or reads JSON, but for stream.ts loading its own module. Answers: no component falls back to words of its own for an engine answer's message."
