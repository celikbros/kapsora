# Windows helper mirroring the Makefile targets for developers without GNU make.
# Usage: .\scripts\dev.ps1 <target>   e.g. .\scripts\dev.ps1 test-unit
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("build", "vet", "fmt", "lint", "test-unit", "test-db", "db-init", "migrate-up", "migrate-version",
                 "run-api", "run-worker", "run-scheduler", "seed-demo", "openapi-generate", "sqlc",
                 "native-install", "native-up", "native-down", "native-status", "web-ci", "web-dev")]
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
    # The demo tenants, accounts and the WP-I7 business scenario. It runs as the application
    # role: .env was just reloaded above, so a migrate-up earlier in this terminal (which points
    # KAPSORA_DATABASE_URL at the schema owner) cannot leak into it. Needs native-up first.
    "seed-demo"        { if (-not $env:KAPSORA_SEED_DEMO_PASSWORD) { $env:KAPSORA_SEED_DEMO_PASSWORD = "demo parola 2026 kapsora" }; go run ./cmd/seed demo }
    "openapi-generate" { Push-Location api/openapi; try { & (Join-Path $gobin "oapi-codegen.exe") -config oapi-codegen.yaml kapsora-v1.yaml } finally { Pop-Location } }
    "sqlc"             { & (Join-Path $gobin "sqlc.exe") generate }
    "native-install"   { & (Join-Path $PSScriptRoot "native\install.ps1") }
    "native-up"        { & (Join-Path $PSScriptRoot "native\up.ps1") }
    "native-down"      { & (Join-Path $PSScriptRoot "native\down.ps1") }
    "native-status"    { & (Join-Path $PSScriptRoot "native\status.ps1") }
    "web-ci"           { pnpm generate; pnpm format; pnpm lint; pnpm typecheck; pnpm test; pnpm build }
    "web-dev"          { pnpm dev }
    "db-init"          {
        $psql = Get-Command psql -ErrorAction SilentlyContinue
        if (-not $psql) { $psql = "C:\Program Files\PostgreSQL\18\bin\psql.exe" }
        & $psql $env:KAPSORA_TEST_ADMIN_DATABASE_URL -v ON_ERROR_STOP=1 -f scripts/db-init.sql
    }
}
