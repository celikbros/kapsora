// seed creates local demo data. It refuses to run against a production-like environment.
//
//	seed account <username> <display name> [email]   creates a login with a random password
//
// The generated password is printed once and never stored in plaintext.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 3 || args[0] != "account" {
		return fmt.Errorf("usage: seed account <username> <display name> [email]")
	}
	cfg, err := config.Load("kapsora-seed")
	if err != nil {
		return err
	}
	if cfg.IsProductionLike() {
		return fmt.Errorf("seed is refused in environment %q", cfg.Environment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.PoolOptions{ApplicationName: "kapsora-seed", MaxConns: 2})
	if err != nil {
		return err
	}
	defer pool.Close()

	svc, err := application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(pool),
		Sessions:    identitypg.NewSessionStore(pool),
		Policy:      domain.DefaultPolicy(),
		Lockout:     domain.DefaultLockout(),
	})
	if err != nil {
		return err
	}

	username, displayName := args[1], args[2]
	email := ""
	if len(args) > 3 {
		email = args[3]
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	actorID, err := svc.CreateAccount(ctx, username, displayName, email, password, true)
	if err != nil {
		return err
	}
	fmt.Printf("actor_id: %s\nusername: %s\npassword: %s\n(must be changed on first use; shown only now)\n",
		actorID, domain.NormalizeUsername(username), password)
	return nil
}

// randomPassword returns 20 base32 characters (100 bits of entropy), grouped for reading.
func randomPassword() (string, error) {
	b := make([]byte, 13)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:20]
	return raw[:5] + "-" + raw[5:10] + "-" + raw[10:15] + "-" + raw[15:20], nil
}
