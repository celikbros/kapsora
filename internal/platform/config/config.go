// Package config loads typed runtime configuration from the environment.
// Secrets never live in the repository; they arrive through the environment
// (local .env via docker compose, Vault/KMS-injected variables in Kubernetes).
package config

import (
	"encoding/hex"
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
	Session         SessionConfig
}

// SessionConfig configures browser sessions and the cookie that carries them
// (v1.2 section 18.2, ADR-022).
type SessionConfig struct {
	// CookieSecure marks the cookie Secure and enables the __Host- name prefix. Only a
	// local HTTP developer setup may turn it off.
	CookieSecure bool
	// SigningKey derives the per-session CSRF token. 32 bytes, hex-encoded in the
	// environment. Required by kapsora-api; other processes may run without it.
	SigningKey       []byte
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration
	StepUpWindow     time.Duration
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
	if cfg.Session, err = loadSession(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadSession(cfg Config) (SessionConfig, error) {
	s := SessionConfig{CookieSecure: true}

	if raw, ok := os.LookupEnv("KAPSORA_COOKIE_SECURE"); ok && raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return s, fmt.Errorf("KAPSORA_COOKIE_SECURE must be true or false: %w", err)
		}
		s.CookieSecure = v
	}
	// An insecure cookie outside local development would send the session id in clear
	// text, so it is refused rather than warned about.
	if !s.CookieSecure && cfg.Environment != EnvLocal && cfg.Environment != EnvTest {
		return s, fmt.Errorf("KAPSORA_COOKIE_SECURE=false is only allowed in the local and test environments, not %s", cfg.Environment)
	}

	if raw := strings.TrimSpace(os.Getenv("KAPSORA_COOKIE_SIGNING_KEY")); raw != "" {
		key, err := hex.DecodeString(raw)
		if err != nil {
			return s, fmt.Errorf("KAPSORA_COOKIE_SIGNING_KEY must be hex: %w", err)
		}
		if len(key) != 32 {
			return s, fmt.Errorf("KAPSORA_COOKIE_SIGNING_KEY must be 32 bytes (64 hex characters), got %d", len(key))
		}
		s.SigningKey = key
	}

	idle, err := envInt("KAPSORA_SESSION_IDLE_MINUTES", 30)
	if err != nil {
		return s, err
	}
	absolute, err := envInt("KAPSORA_SESSION_ABSOLUTE_HOURS", 8)
	if err != nil {
		return s, err
	}
	stepUp, err := envInt("KAPSORA_STEP_UP_MINUTES", 10)
	if err != nil {
		return s, err
	}
	s.IdleTimeout = time.Duration(idle) * time.Minute
	s.AbsoluteLifetime = time.Duration(absolute) * time.Hour
	s.StepUpWindow = time.Duration(stepUp) * time.Minute
	return s, nil
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
