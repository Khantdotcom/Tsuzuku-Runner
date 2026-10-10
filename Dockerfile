# syntax=docker/dockerfile:1

# Builds the Go binaries. Pick one with --target: server, worker, or migrate.

FROM golang:1.27 AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/server ./cmd/worker ./cmd/migrate
RUN mkdir -p /out/artifacts

FROM gcr.io/distroless/static-debian13:nonroot AS server
COPY --from=build /out/server /usr/local/bin/server
# Owned by the nonroot user so the server can write evidence files. A named
# volume mounted here starts with this ownership.
COPY --from=build --chown=65532:65532 /out/artifacts /var/lib/tsuzuku/artifacts
ENV TSUZUKU_ARTIFACT_DIR=/var/lib/tsuzuku/artifacts
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]

FROM gcr.io/distroless/static-debian13:nonroot AS worker
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/worker"]

FROM gcr.io/distroless/static-debian13:nonroot AS migrate
COPY --from=build /out/migrate /usr/local/bin/migrate
ENTRYPOINT ["/usr/local/bin/migrate"]
CMD ["up"]
