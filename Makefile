.PHONY: test build run compose-up compose-down

GO_TAGS := fts5 sqlite_omit_load_extension

test:
	go test -tags "$(GO_TAGS)" ./...

build:
	CGO_ENABLED=1 go build -tags "$(GO_TAGS)" -trimpath -o bin/blog ./cmd/blog

run:
	go run -tags "$(GO_TAGS)" ./cmd/blog serve --config config.example.toml

compose-up:
	docker compose up --build

compose-down:
	docker compose down
