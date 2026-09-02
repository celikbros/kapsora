# Multi-stage build producing one minimal image with all KAPSORA binaries.
# The command selects the process: /app/api, /app/worker, /app/scheduler, /app/migrate.
# Base images are pinned by tag here and by digest in CI/production manifests.

FROM golang:1.27 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN mkdir -p /out \
 && go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api \
 && go build -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && go build -ldflags="-s -w" -o /out/scheduler ./cmd/scheduler \
 && go build -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /app/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT []
CMD ["/app/api"]
