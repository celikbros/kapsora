# Windows helper mirroring the Makefile targets for developers without GNU make.
# Usage: .\scripts\dev.ps1 <target>   e.g. .\scripts\dev.ps1 test-unit
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("build", "vet", "fmt", "lint", "test-unit", "test-db", "migrate-up", "migrate-version",
                 "run-api", "run-worker", "run-scheduler", "openapi-generate", "sqlc", "dev-up", "dev-down")]
    [string]$Target
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# Load .env into the process environment (KEY=VALUE lines, # comments).
if (Test-Path ".env") {
    Get-Content ".env" | ForEach-Object {
        if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)\s*$') {
            [Environment]::SetEnvironmentVariable($matches[1], $matches[2])
        }
    }
}

$gobin = Join-Path (go env GOPATH) "bin"

switch ($Target) {
    "build"            { go build -o bin/ ./cmd/... }
    "vet"              { go vet ./... }
    "fmt"              { go fmt ./... }
    "lint"             { & (Join-Path $gobin "golangci-lint.exe") run ./... }
    # -race needs cgo (a C toolchain) on Windows; CI runs the race detector on Linux.
    "test-unit"        { go test -count=1 ./internal/... ./cmd/... }
    "test-db"          { go test -count=1 -v ./db/tests/... }
    "migrate-up"       { $env:KAPSORA_DATABASE_URL = $env:KAPSORA_MIGRATE_DATABASE_URL; go run ./cmd/migrate up }
    "migrate-version"  { $env:KAPSORA_DATABASE_URL = $env:KAPSORA_MIGRATE_DATABASE_URL; go run ./cmd/migrate version }
    "run-api"          { go run ./cmd/api }
    "run-worker"       { go run ./cmd/worker }
    "run-scheduler"    { go run ./cmd/scheduler }
    "openapi-generate" { & (Join-Path $gobin "oapi-codegen.exe") -config api/openapi/oapi-codegen.yaml api/openapi/kapsora-v1.yaml }
    "sqlc"             { & (Join-Path $gobin "sqlc.exe") generate }
    "dev-up"           { docker compose up -d postgres keycloak valkey minio minio-init clamav mailpit otel-collector; docker compose --profile migrate run --rm migrate }
    "dev-down"         { docker compose --profile app --profile migrate down }
}
