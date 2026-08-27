#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${DIST_DIR:-"$ROOT/dist"}
mkdir -p "$DIST"
{
	echo "Personal Blog dependency license review"
	echo "Project license: Apache-2.0 (LICENSE)"
	echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	if command -v go-licenses >/dev/null 2>&1; then
		go-licenses report ./... || true
	else
		echo "go-licenses is not installed; direct and transitive module graph follows."
		if command -v go >/dev/null 2>&1; then
			go list -m all
		else
			sed -n '/^require (/,/^)/p' "$ROOT/go.mod"
		fi
	fi
	echo "Manual compatibility rule: Apache-2.0, BSD, MIT, ISC and MPL-2.0 are accepted; copyleft or unknown licenses require release review."
} > "$DIST/LICENSE-REVIEW.txt"
echo "license review written to $DIST/LICENSE-REVIEW.txt"
