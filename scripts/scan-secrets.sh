#!/bin/sh
image=${1:?usage: scan-secrets.sh <gitleaks image>}
cd "$(dirname -- "$0")/.." || exit 1
copy=$(mktemp -d "${TMPDIR:-/tmp}/secrets.XXXXXX") || exit 1
trap 'rm -rf "$copy"' EXIT

sh scripts/files.sh >"$copy/list" || exit 1
mkdir "$copy/tree" || exit 1
tar --null -cf - -T "$copy/list" | tar -xf - -C "$copy/tree" || exit 1
docker run --rm --network none -v "$copy/tree:/scan:ro" -w /scan "$image" \
	dir --config .gitleaks.toml --redact --no-banner --verbose .
