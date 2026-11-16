#!/bin/sh
set -eu
template=$(dirname -- "$0")/install.sh
usage='usage: scripts/release-install.sh <version> <compose.yaml> <compose.release.yaml with the digest> <output file>'
[ $# = 4 ] || {
	echo "$usage" >&2
	exit 2
}
version=$1
compose=$2
release=$3
out=$4
printf '%s\n' "$version" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || {
	echo "release-install: not a version MAJOR.MINOR.PATCH: $version" >&2
	exit 1
}
for f in "$compose" "$release"; do
	[ -s "$f" ] || {
		echo "release-install: no such file, or empty: $f" >&2
		exit 1
	}
done
if grep -qF '@sha256:0000000000000000000000000000000000000000000000000000000000000000' "$release"; then
	echo "release-install: $release still names the placeholder image; write it with scripts/release-compose.sh" >&2
	exit 1
fi
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum "$1")
	else
		sum=$(shasum -a 256 "$1")
	fi
	printf '%s\n' "${sum%% *}"
}
compose_sum=$(sha256 "$compose")
release_sum=$(sha256 "$release")
for s in "$compose_sum" "$release_sum"; do
	printf '%s\n' "$s" | grep -Eqx '[0-9a-f]{64}' || {
		echo "release-install: not a SHA-256: $s" >&2
		exit 1
	}
done
for p in "version='@VERSION@'" "compose_sha256='@COMPOSE_SHA256@'" "release_sha256='@RELEASE_SHA256@'"; do
	[ "$(grep -cxF "$p" "$template")" = 1 ] || {
		echo "release-install: $template doesn't hold the line $p exactly once" >&2
		exit 1
	}
done
sed -e "s|^version='@VERSION@'\$|version='$version'|" \
	-e "s|^compose_sha256='@COMPOSE_SHA256@'\$|compose_sha256='$compose_sum'|" \
	-e "s|^release_sha256='@RELEASE_SHA256@'\$|release_sha256='$release_sum'|" "$template" >"$out"
if grep -q '@[A-Z0-9_]*@' "$out" ||
	[ "$(grep -cxF "version='$version'" "$out")" != 1 ] ||
	[ "$(grep -cxF "compose_sha256='$compose_sum'" "$out")" != 1 ] ||
	[ "$(grep -cxF "release_sha256='$release_sum'" "$out")" != 1 ] ||
	! sh -n "$out"; then
	echo "release-install: $out doesn't carry the version and both SHA-256 sums in place of the placeholders" >&2
	rm -f "$out"
	exit 1
fi
echo "release-install: wrote $out for $version, compose.yaml $compose_sum, compose.release.yaml $release_sum"
