#!/bin/sh
set -eu
umask 077

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
state=${PROCTOR_SF_STATE:-/tmp/sf-proctor}
authoritative=${PROCTOR_SF_AUTHORITATIVE:-0}
checkout=${PROCTOR_SF_CHECKOUT:-$repo}

say() { printf 'sf-proctor: %s\n' "$*" >&2; }

keep=0
n=$#
while [ "$n" -gt 0 ]; do
	a=$1
	shift
	n=$((n - 1))
	case "$a" in
		--keep) keep=1 ;;
		-r | --repo | --repo=* | -b | --binding | --binding=* | --report | --report=*)
			say "$a is set by this script; the report is $state/report.json"
			exit 2
			;;
		--authoritative | --authoritative=*)
			say "set PROCTOR_SF_AUTHORITATIVE=1 instead of $a, so up.sh starts the loaded images rather than building its own"
			exit 2
			;;
		*) set -- "$@" "$a" ;;
	esac
done

case "$authoritative" in
	0) ;;
	1) set -- --authoritative "$@" ;;
	*)
		say "PROCTOR_SF_AUTHORITATIVE must be 0 or 1, got $authoritative"
		exit 2
		;;
esac
case "$checkout" in
	/*) ;;
	*)
		say "PROCTOR_SF_CHECKOUT must be an absolute path, got $checkout"
		exit 2
		;;
esac
[ -d "$checkout" ] || {
	say "PROCTOR_SF_CHECKOUT isn't a folder: $checkout"
	exit 2
}
export PROCTOR_SF_CHECKOUT="$checkout"

if [ -n "${PROCTOR:-}" ]; then
	[ -f "$PROCTOR" ] && [ -x "$PROCTOR" ] || {
		say "PROCTOR=$PROCTOR isn't an executable file"
		exit 2
	}
	proctor_bin=$PROCTOR
else
	proctor_bin=$(command -v proctor 2>/dev/null) || {
		say "proctor isn't on PATH; set PROCTOR to the proctor binary of a build with the features .claude/rules/proctor.md names"
		exit 2
	}
fi
say "Proctor $("$proctor_bin" version 2>/dev/null || echo unknown) at $proctor_bin"

needed="app_logs app_logs.logfmt approved_logs cleanup docker exe09.related_guard exe09.waivers_guard_nothing exe11.fact_paths http.body_base64 http.body_fill http.destructive http.reads http.shows http.stream matchers.differs matchers.excludes matchers.matches matchers.matches_numbers matchers.overflow policy_from relations report.always step.timeout tables.covers time14.accumulates transcripts waive.rows"
if ! features=$("$proctor_bin" version --features 2>/dev/null); then
	say "this Proctor build can't list its features (proctor version --features), so it is older than the specifications need; set PROCTOR to a newer build, one with the features $needed (.claude/rules/proctor.md)"
	exit 2
fi
missing=""
for f in $needed; do
	printf '%s\n' "$features" | grep -qx "$f" || missing="$missing $f"
done
if [ -n "$missing" ]; then
	say "this Proctor build lacks the features$missing, which proctor/proctor.yaml and the specifications use; set PROCTOR to a build that has them (.claude/rules/proctor.md)"
	exit 2
fi

sh "$here/routes.sh"

set +e
checked=$(cd "$checkout" && "$proctor_bin" validate -r . 2>&1)
code=$?
set -e
if [ "$code" -ne 0 ]; then
	printf '%s\n' "$checked" >&2
	exit "$code"
fi

started=0
finish() {
	if [ "$started" -eq 0 ]; then
		return
	fi
	if [ "$keep" -eq 1 ]; then
		say "--keep: the stack stays up on 127.0.0.1:${PROCTOR_SF_PORT:-17799}; stop it with sh $here/down.sh"
	else
		sh "$here/down.sh" || true
	fi
}
trap finish EXIT

sh "$here/up.sh"
started=1

set +e
(
	set -a
	. "$state/proctor.env"
	set +a
	cd "$checkout"
	exec "$proctor_bin" run -r . -b "$state/binding.yaml" --report "$state/report.json" "$@"
)
code=$?
set -e
say "proctor exit $code"

leaked=""
for f in "$state/report.json" "$state/engine.log"; do
	if [ -f "$f" ] && grep -qF -f "$state/scrub-patterns" "$f"; then
		leaked="$leaked $f"
	fi
done
if [ -n "$leaked" ]; then
	say "a credential of this run is in:$leaked; delete the file before sharing anything from $state"
	[ "$code" -ne 0 ] || code=1
else
	say "scrub check passed: no credential of this run in $state/report.json or $state/engine.log"
fi
errors=$(grep -c 'level=ERROR' "$state/engine.log" 2>/dev/null || true)
say "the engine logged ${errors:-0} level=ERROR lines ($state/engine.log), a count beside Proctor's app-log check, which fails the scenario during which a line was written"
exit "$code"
