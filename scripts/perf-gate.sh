#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if command -v go >/dev/null 2>&1; then
	cd "$ROOT"
	go test -tags 'fts5 sqlite_omit_load_extension' ./internal/perf -run 'TestADR31Budgets|TestPhase3ScaleProjections' -count=1 -v
	go test -tags 'fts5 sqlite_omit_load_extension' ./internal/perf -bench=. -benchmem -run '^$' -count=1
else
	docker run --rm -v "$ROOT:/workspace" -w /workspace golang:1.26.0-bookworm sh -ec 'go test -tags "fts5 sqlite_omit_load_extension" ./internal/perf -run "TestADR31Budgets|TestPhase3ScaleProjections" -count=1 -v; go test -tags "fts5 sqlite_omit_load_extension" ./internal/perf -bench=. -benchmem -run "^$" -count=1'
fi
echo "ADR-0031 and phase-three scale performance gates passed"
