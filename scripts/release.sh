#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${DIST_DIR:-"$ROOT/dist"}
VERSION=${VERSION:-dev}
COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || printf unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
IMAGE=${IMAGE:-personal-blog:$VERSION}
PLATFORMS=${PLATFORMS:-linux/amd64,linux/arm64}
mkdir -p "$DIST"

if command -v go >/dev/null 2>&1; then
	go test -tags 'fts5 sqlite_omit_load_extension' ./...
	go vet -tags 'fts5 sqlite_omit_load_extension' ./...
else
	echo "go is not installed; release tests must be run in the pinned Go container" >&2
fi

if command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
	OUTPUT="type=oci,dest=$DIST/personal-blog-$VERSION.oci.tar"
	set -- docker buildx build --platform "$PLATFORMS" --tag "$IMAGE" \
		--build-arg "VERSION=$VERSION" --build-arg "COMMIT=$COMMIT" \
		--build-arg "BUILD_TIME=$BUILD_TIME" --provenance=false --sbom=false
	if [ "${PUSH:-0}" = "1" ]; then
		"$@" --push "$ROOT"
	else
		"$@" --output "$OUTPUT" "$ROOT"
	fi
else
	echo "docker buildx is unavailable; skipped multi-architecture image build" >&2
fi

"$ROOT/scripts/generate-sbom.sh"
"$ROOT/scripts/license-audit.sh"

if command -v shasum >/dev/null 2>&1; then
	(cd "$DIST" && find . -maxdepth 1 -type f ! -name SHA256SUMS -print0 | xargs -0 shasum -a 256 > SHA256SUMS)
elif command -v sha256sum >/dev/null 2>&1; then
	(cd "$DIST" && find . -maxdepth 1 -type f ! -name SHA256SUMS -print0 | xargs -0 sha256sum > SHA256SUMS)
else
	echo "neither shasum nor sha256sum is available" >&2
	exit 1
fi
echo "release artifacts written to $DIST"
