package config

import (
	"os"
	"strings"
	"testing"
)

// isolate removes every KAPSORA_* variable for the duration of one test. Developers keep
// their local .env exported in the shell they run tests from, and these tests assert what
// Load does with an empty environment, so they must not read the developer's values.
func isolate(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "KAPSORA_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	isolate(t)
	t.Setenv("KAPSORA_DATABASE_URL", "")
	if _, err := Load("kapsora-api"); err == nil {
		t.Fatalf("expected error without KAPSORA_DATABASE_URL")
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	isolate(t)
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

func TestInvitationDeliveryRequiresExplicitLoopback(t *testing.T) {
	isolate(t)
	t.Setenv("KAPSORA_DATABASE_URL", "postgres://u:p@localhost:5432/kapsora")
	cfg, err := Load("kapsora-worker")
	if err != nil || cfg.Invitations.DeliveryEnabled {
		t.Fatal("invitation delivery must default off")
	}
	t.Setenv("KAPSORA_INVITATION_DELIVERY_MODE", "local-loopback")
	cfg, err = Load("kapsora-worker")
	if err != nil || !cfg.Invitations.DeliveryEnabled {
		t.Fatal("explicit loopback mode rejected")
	}
	t.Setenv("KAPSORA_SMTP_ADDR", "smtp.example.test:25")
	if _, err = Load("kapsora-worker"); err == nil {
		t.Fatal("external SMTP accepted in local mode")
	}
	t.Setenv("KAPSORA_SMTP_ADDR", "127.0.0.1:1025")
	t.Setenv("KAPSORA_INVITATION_LINK_BASE", "javascript:alert(1)")
	if _, err = Load("kapsora-worker"); err == nil {
		t.Fatal("invalid invitation link base accepted")
	}
	t.Setenv("KAPSORA_INVITATION_LINK_BASE", "http://127.0.0.1:5181/")
	cfg, err = Load("kapsora-worker")
	if err != nil || cfg.Invitations.LinkBase != "http://127.0.0.1:5181" {
		t.Fatal("fixed invitation origin not normalized")
	}
	t.Setenv("KAPSORA_ENV", "production")
	if _, err = Load("kapsora-worker"); err == nil {
		t.Fatal("production loopback invitation mode accepted")
	}
}
