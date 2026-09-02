# KAPSORA developer entry points. Works with GNU make on Linux/macOS/Git Bash.
# On Windows without make, run the underlying commands from scripts/dev.ps1.

SHELL := /bin/sh
GO ?= go
GOBIN ?= $(shell $(GO) env GOPATH)/bin
DOCKER_COMPOSE ?= docker compose
-include .env
export

.PHONY: help dev-up dev-down dev-logs migrate-up migrate-version build run-api run-worker run-scheduler \
        test test-unit test-db lint vet fmt openapi-lint openapi-generate openapi-diff sqlc tools ci

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

## --- local stack -------------------------------------------------------------------
dev-up: ## Start infrastructure and apply migrations
	$(DOCKER_COMPOSE) up -d postgres keycloak valkey minio minio-init clamav mailpit otel-collector
	$(DOCKER_COMPOSE) --profile migrate run --rm migrate

dev-up-all: ## Start infrastructure plus api/worker/scheduler containers
	$(DOCKER_COMPOSE) --profile app up -d --build

dev-down: ## Stop the stack (keeps volumes)
	$(DOCKER_COMPOSE) --profile app --profile migrate down

dev-logs: ## Tail stack logs
	$(DOCKER_COMPOSE) logs -f --tail=200

## --- database ----------------------------------------------------------------------
migrate-up: ## Apply migrations with the owner role from .env
	KAPSORA_DATABASE_URL="$(KAPSORA_MIGRATE_DATABASE_URL)" $(GO) run ./cmd/migrate up

migrate-version: ## Print schema version
	KAPSORA_DATABASE_URL="$(KAPSORA_MIGRATE_DATABASE_URL)" $(GO) run ./cmd/migrate version

## --- build and run -----------------------------------------------------------------
build: ## Build all binaries into ./bin
	$(GO) build -o bin/ ./cmd/...

run-api: ## Run the API from source
	$(GO) run ./cmd/api

run-worker: ## Run the worker from source
	$(GO) run ./cmd/worker

run-scheduler: ## Run the scheduler from source
	$(GO) run ./cmd/scheduler

## --- quality -----------------------------------------------------------------------
fmt: ## gofmt all packages
	$(GO) fmt ./...

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint (install with `make tools`)
	$(GOBIN)/golangci-lint run ./...

test: test-unit test-db ## Unit + schema tests

test-unit: ## Unit tests with race detector
	$(GO) test -race -count=1 ./internal/... ./cmd/...

test-db: ## PostgreSQL schema tests (needs KAPSORA_TEST_ADMIN_DATABASE_URL)
	$(GO) test -count=1 -v ./db/tests/...

## --- contracts ---------------------------------------------------------------------
openapi-lint: ## Spectral lint of the OpenAPI contract
	npx --yes @stoplight/spectral-cli lint api/openapi/kapsora-v1.yaml --ruleset .spectral.yaml

openapi-generate: ## Generate Go server types from the contract
	$(GOBIN)/oapi-codegen -config api/openapi/oapi-codegen.yaml api/openapi/kapsora-v1.yaml

openapi-diff: ## Breaking-change check against main
	git show main:api/openapi/kapsora-v1.yaml > /tmp/kapsora-v1.main.yaml 2>/dev/null && \
	$(GOBIN)/oasdiff breaking /tmp/kapsora-v1.main.yaml api/openapi/kapsora-v1.yaml --fail-on ERR || true

sqlc: ## Generate type-safe query code
	$(GOBIN)/sqlc generate

tools: ## Install Go-based developer tools into GOPATH/bin
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	$(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	$(GO) install github.com/oasdiff/oasdiff@latest
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest

ci: fmt vet lint test-unit openapi-generate ## What CI runs locally (db tests need a database)
	git diff --exit-code -- api/generated
