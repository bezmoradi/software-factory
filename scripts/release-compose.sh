#!/bin/sh
set -eu
src=$(dirname -- "$0")/../compose.release.yaml
placeholder='ghcr.io/bezmoradi/software-factory@sha256:0000000000000000000000000000000000000000000000000000000000000000'
usage='usage: scripts/release-compose.sh <image without tag or digest> <sha256:digest> <output file>'
[ $# = 3 ] || {
	echo "$usage" >&2
	exit 2
}
image=$1
digest=$2
out=$3
printf '%s\n' "$image" | grep -Eqx '[a-z0-9][a-z0-9._-]*(:[0-9]+)?(/[a-z0-9][a-z0-9._-]*)+' || {
	echo "release-compose: not an image name without a tag or digest: $image" >&2
	exit 1
}
printf '%s\n' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' || {
	echo "release-compose: not a sha256 digest: $digest" >&2
	exit 1
}
[ "$image@$digest" != "$placeholder" ] || {
	echo "release-compose: the digest is the placeholder's" >&2
	exit 1
}
[ "$(grep -cF "$placeholder" "$src")" = 1 ] || {
	echo "release-compose: compose.release.yaml doesn't hold the placeholder image exactly once" >&2
	exit 1
}
sed "s|$placeholder|$image@$digest|" "$src" >"$out"
if grep -qF "$placeholder" "$out" || [ "$(grep -cF "image: $image@$digest" "$out")" != 1 ]; then
	echo "release-compose: $out doesn't name $image@$digest in place of the placeholder" >&2
	rm -f "$out"
	exit 1
fi
echo "release-compose: wrote $out with $image@$digest"
