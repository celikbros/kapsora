// Package config loads typed runtime configuration from the environment.
// Secrets never live in the repository; they arrive through the environment
// (local .env via docker compose, Vault/KMS-injected variables in Kubernetes).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names are closed: they gate behaviour such as debug endpoints.
const (
	EnvLocal       = "local"
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config is shared by all three processes. Process-specific settings are added
// as nested structs when a process needs them.
type Config struct {
	ServiceName     string
	Environment     string
	Version         string
	HTTPAddr        string
	DatabaseURL     string
	DBMaxConns      int32
	LogLevel        string
	ShutdownTimeout time.Duration
}

// Load reads KAPSORA_* variables. serviceName is the binary name (kapsora-api, ...).
func Load(serviceName string) (Config, error) {
	cfg := Config{
		ServiceName:     serviceName,
		Environment:     envOr("KAPSORA_ENV", EnvLocal),
		Version:         envOr("KAPSORA_VERSION", "dev"),
		HTTPAddr:        envOr("KAPSORA_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("KAPSORA_DATABASE_URL"),
		LogLevel:        strings.ToLower(envOr("KAPSORA_LOG_LEVEL", "info")),
		ShutdownTimeout: 15 * time.Second,
	}

	maxConns, err := envInt("KAPSORA_DB_MAX_CONNS", 10)
	if err != nil {
		return cfg, err
	}
	if maxConns < 1 || maxConns > 1000 {
		return cfg, fmt.Errorf("KAPSORA_DB_MAX_CONNS must be between 1 and 1000, got %d", maxConns)
	}
	cfg.DBMaxConns = int32(maxConns)

	switch cfg.Environment {
	case EnvLocal, EnvDevelopment, EnvTest, EnvStaging, EnvProduction:
	default:
		return cfg, fmt.Errorf("KAPSORA_ENV %q is not one of local, development, test, staging, production", cfg.Environment)
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("KAPSORA_DATABASE_URL is required")
	}
	return cfg, nil
}

// IsProductionLike reports whether debug conveniences must be disabled.
func (c Config) IsProductionLike() bool {
	return c.Environment == EnvStaging || c.Environment == EnvProduction
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}
