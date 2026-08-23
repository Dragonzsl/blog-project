# syntax=docker/dockerfile:1.7

FROM golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build \
      -tags "fts5 sqlite_omit_load_extension" \
      -trimpath \
      -ldflags "-s -w -X github.com/zhushilin/blog-project/internal/buildinfo.Version=${VERSION} -X github.com/zhushilin/blog-project/internal/buildinfo.Commit=${COMMIT} -X github.com/zhushilin/blog-project/internal/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/blog ./cmd/blog

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f

COPY --from=build /out/blog /blog
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/blog"]
CMD ["serve"]
