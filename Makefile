.PHONY: test test-race vet build run deploy compose-up compose-down perf-gate stage3-acceptance release sbom license-audit browser

GO_TAGS := fts5 sqlite_omit_load_extension

test:
	go test -tags "$(GO_TAGS)" ./...

test-race:
	go test -race -tags "$(GO_TAGS)" ./...

vet:
	go vet -tags "$(GO_TAGS)" ./...

build:
	CGO_ENABLED=1 go build -tags "$(GO_TAGS)" -trimpath -o bin/blog ./cmd/blog

run:
	go run -tags "$(GO_TAGS)" ./cmd/blog serve --config config.example.toml

deploy:
	./scripts/deploy.sh

compose-up:
	docker compose up --build

compose-down:
	docker compose down

perf-gate:
	./scripts/perf-gate.sh

stage3-acceptance:
	./scripts/stage3-acceptance.sh

release:
	./scripts/release.sh

sbom:
	./scripts/generate-sbom.sh

license-audit:
	./scripts/license-audit.sh

browser:
	./scripts/browser-regression.sh
