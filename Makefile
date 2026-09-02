# KAPSORA developer entry points (GNU make on Linux/macOS/Git Bash).
# Container-free runtime (ADR-021): PostgreSQL, Keycloak and the other services run as
# native processes; see docs/runbooks/local-native-environment.md.
# On Windows without make, scripts/dev.ps1 mirrors these targets.

SHELL := /bin/sh
GO ?= go
GOBIN ?= $(shell $(GO) env GOPATH)/bin
PSQL ?= psql
-include .env
export

.PHONY: help db-init migrate-up migrate-version build run-api run-worker run-scheduler \
        test test-unit test-db lint vet fmt openapi-lint openapi-generate openapi-diff sqlc tools ci

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

## --- database ----------------------------------------------------------------------
db-init: ## Create the kapsora_app role and kapsora database on a local PostgreSQL 18 (needs KAPSORA_TEST_ADMIN_DATABASE_URL)
	$(PSQL) "$(KAPSORA_TEST_ADMIN_DATABASE_URL)" -v ON_ERROR_STOP=1 -f scripts/db-init.sql

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

test-unit: ## Unit tests (CI adds -race on Linux)
	$(GO) test -count=1 ./internal/... ./cmd/...

test-db: ## PostgreSQL schema tests (needs KAPSORA_TEST_ADMIN_DATABASE_URL)
	$(GO) test -count=1 -v ./db/tests/...

## --- contracts ---------------------------------------------------------------------
openapi-lint: ## Spectral lint of the OpenAPI contract
	npx --yes @stoplight/spectral-cli lint api/openapi/kapsora-v1.yaml --ruleset .spectral.yaml

openapi-generate: ## Generate Go server types from the contract
	cd api/openapi && $(GOBIN)/oapi-codegen -config oapi-codegen.yaml kapsora-v1.yaml

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

ci: fmt vet lint test-unit openapi-generate sqlc ## What CI runs locally (db tests need a database)
	git diff --exit-code -- api/generated internal/platform/sqlcgen
