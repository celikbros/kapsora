package config

import "testing"

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("KAPSORA_DATABASE_URL", "")
	if _, err := Load("kapsora-api"); err == nil {
		t.Fatalf("expected error without KAPSORA_DATABASE_URL")
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("KAPSORA_DATABASE_URL", "postgres://u:p@localhost:5432/kapsora")
	t.Setenv("KAPSORA_ENV", "")
	t.Setenv("KAPSORA_DB_MAX_CONNS", "25")

	cfg, err := Load("kapsora-api")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Environment != EnvLocal || cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 25 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.IsProductionLike() {
		t.Fatalf("local must not be production-like")
	}

	t.Setenv("KAPSORA_ENV", "moon")
	if _, err := Load("kapsora-api"); err == nil {
		t.Fatalf("expected error for unknown environment")
	}

	t.Setenv("KAPSORA_ENV", "production")
	t.Setenv("KAPSORA_DB_MAX_CONNS", "lots")
	if _, err := Load("kapsora-api"); err == nil {
		t.Fatalf("expected error for non-integer max conns")
	}

	t.Setenv("KAPSORA_DB_MAX_CONNS", "0")
	if _, err := Load("kapsora-api"); err == nil {
		t.Fatalf("expected error for out-of-range max conns")
	}
}
