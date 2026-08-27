#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ ! -d "$ROOT/tests/browser" ]; then
	echo "browser fixtures are missing" >&2
	exit 1
fi
if command -v npx >/dev/null 2>&1 && npx --yes @playwright/test --version >/dev/null 2>&1; then
	cd "$ROOT"
	NPM_CACHE=$(npm config get cache)
	PLAYWRIGHT_PACKAGE_DIR=$(ls -td "$NPM_CACHE"/_npx/*/node_modules/@playwright/test 2>/dev/null | head -n 1 || true)
	if [ -z "$PLAYWRIGHT_PACKAGE_DIR" ]; then
		echo "Playwright Test package path could not be resolved" >&2
		exit 1
	fi
	PLAYWRIGHT_NODE_MODULES=$(dirname "$(dirname "$PLAYWRIGHT_PACKAGE_DIR")")
	NODE_PATH="$PLAYWRIGHT_NODE_MODULES${NODE_PATH:+:$NODE_PATH}" "$PLAYWRIGHT_NODE_MODULES/.bin/playwright" test tests/browser --config=tests/browser/playwright.config.ts
	printf '%s\n' "Chromium, Firefox and WebKit regression suite passed."
	else
	if [ "${BROWSER_STRICT:-0}" = "1" ]; then
		echo "Playwright is unavailable; install it and rerun browser-regression.sh" >&2
		exit 1
	fi
	echo "Playwright is unavailable; browser suite not run (set BROWSER_STRICT=1 in release CI)."
fi
