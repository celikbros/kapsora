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
	Documents       DocumentConfig
}

// DocumentConfig configures the object store a file actually lives in and the malware
// scanner that decides whether it may (WP-I4-04, ADR-021: both run as native services,
// there is no container anywhere in this project).
//
// Everything here has a working local default except the store credentials, which is why
// Load never fails on it: the API and the worker check Configured themselves and refuse to
// start without a store, while the scheduler and the tooling do not need one.
type DocumentConfig struct {
	// Endpoint is the S3-compatible base URL, for example http://127.0.0.1:9000. A bare
	// host:port is read as http.
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	// QuarantineBucket is where an upload lands and is scanned. Nothing is ever readable
	// from it: no presigned GET is minted against it anywhere in the codebase.
	QuarantineBucket string
	// SecureBucket is where a clean file is promoted to, and the only bucket a download
	// URL is ever signed for.
	SecureBucket string
	// UploadURLTTL and DownloadURLTTL are how long a presigned URL lives. Both are short
	// because a presigned URL is a bearer credential.
	UploadURLTTL   time.Duration
	DownloadURLTTL time.Duration
	// EncryptionKeyRef names the key the store protects the bytes with. It is recorded on
	// every version; it is a reference, never key material.
	EncryptionKeyRef string
	// ScannerAddr is the clamd TCP socket, host:port.
	ScannerAddr string
	// ScannerTimeout bounds one whole scan, connection included.
	ScannerTimeout time.Duration
	// RetentionDays is how long a stored document is kept before the retention sweep
	// removes its bytes. Zero disables the sweep, which is the default: deleting real
	// documents after a number nobody chose is worse than keeping them.
	RetentionDays int
}

// Configured reports whether an object store was configured. Credentials are the test:
// everything else has a local default, and a store nobody gave a key for is a store the
// process cannot talk to.
func (d DocumentConfig) Configured() bool {
	return d.Endpoint != "" && d.AccessKey != "" && d.SecretKey != ""
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
	if cfg.Documents, err = loadDocuments(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// loadDocuments reads the object store and scanner settings. It fails only on a value that
// is present and unusable: a missing store is a decision the process makes, not a
// configuration error, because two of the three binaries have no use for one.
func loadDocuments() (DocumentConfig, error) {
	d := DocumentConfig{
		Endpoint:         envOr("KAPSORA_MINIO_ADDR", "127.0.0.1:9000"),
		Region:           envOr("KAPSORA_OBJECT_STORE_REGION", "us-east-1"),
		AccessKey:        os.Getenv("KAPSORA_MINIO_ROOT_USER"),
		SecretKey:        os.Getenv("KAPSORA_MINIO_ROOT_PASSWORD"),
		QuarantineBucket: envOr("KAPSORA_DOCUMENT_QUARANTINE_BUCKET", "quarantine"),
		SecureBucket:     envOr("KAPSORA_DOCUMENT_SECURE_BUCKET", "secure"),
		EncryptionKeyRef: envOr("KAPSORA_DOCUMENT_ENCRYPTION_KEY_REF", "objectstore:default"),
		ScannerAddr:      envOr("KAPSORA_CLAMAV_ADDR", "127.0.0.1:3310"),
	}

	upload, err := envInt("KAPSORA_DOCUMENT_UPLOAD_URL_MINUTES", 15)
	if err != nil {
		return d, err
	}
	download, err := envInt("KAPSORA_DOCUMENT_DOWNLOAD_URL_MINUTES", 5)
	if err != nil {
		return d, err
	}
	scan, err := envInt("KAPSORA_DOCUMENT_SCAN_TIMEOUT_SECONDS", 120)
	if err != nil {
		return d, err
	}
	retention, err := envInt("KAPSORA_DOCUMENT_RETENTION_DAYS", 0)
	if err != nil {
		return d, err
	}
	// A presigned URL is a bearer credential, so its life is bounded here rather than left
	// to whatever an operator typed. A day is already generous for both.
	if upload < 1 || upload > 1440 {
		return d, fmt.Errorf("KAPSORA_DOCUMENT_UPLOAD_URL_MINUTES must be between 1 and 1440, got %d", upload)
	}
	if download < 1 || download > 1440 {
		return d, fmt.Errorf("KAPSORA_DOCUMENT_DOWNLOAD_URL_MINUTES must be between 1 and 1440, got %d", download)
	}
	if scan < 1 || scan > 3600 {
		return d, fmt.Errorf("KAPSORA_DOCUMENT_SCAN_TIMEOUT_SECONDS must be between 1 and 3600, got %d", scan)
	}
	if retention < 0 {
		return d, fmt.Errorf("KAPSORA_DOCUMENT_RETENTION_DAYS must not be negative, got %d", retention)
	}
	d.UploadURLTTL = time.Duration(upload) * time.Minute
	d.DownloadURLTTL = time.Duration(download) * time.Minute
	d.ScannerTimeout = time.Duration(scan) * time.Second
	d.RetentionDays = retention
	return d, nil
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
