#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE_URL=${BASE_URL:-https://localhost}
CURL="curl -fsS"
if [ "${CURL_INSECURE:-1}" = "1" ]; then CURL="curl -kfsS"; fi

cd "$ROOT"
if command -v go >/dev/null 2>&1; then
	go test -tags 'fts5 sqlite_omit_load_extension' ./...
	go test -race -tags 'fts5 sqlite_omit_load_extension' ./...
	go vet -tags 'fts5 sqlite_omit_load_extension' ./...
	go mod verify
else
	docker run --rm -v "$ROOT:/workspace" -w /workspace golang:1.26.0-bookworm sh -ec 'go test -tags "fts5 sqlite_omit_load_extension" ./...; go test -race -tags "fts5 sqlite_omit_load_extension" ./...; go vet -tags "fts5 sqlite_omit_load_extension" ./...; go mod verify'
fi

if command -v docker >/dev/null 2>&1; then
	docker compose config >/dev/null
	if docker compose ps --status running --services | grep -qx caddy; then
		$CURL "$BASE_URL/livez" >/dev/null
		$CURL "$BASE_URL/readyz" >/dev/null
		$CURL "$BASE_URL/" >/dev/null
		status=$(curl -k -s -o /dev/null -w '%{http_code}' "$BASE_URL/admin/plugins")
		case "$status" in 200|303) ;; *) echo "unexpected admin protection status: $status" >&2; exit 1 ;; esac
	fi
fi

echo "Stage three acceptance checks passed"
