// migrate applies the embedded schema migrations.
//
//	migrate up        apply pending migrations
//	migrate version   print current version
//	migrate drop      drop all objects (refused outside local/test)
//
// The database URL comes from KAPSORA_DATABASE_URL. Use a role that owns the
// schema and may bypass RLS (never the application role).
package main

import (
	"fmt"
	"os"

	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: migrate up|version|drop")
	}
	cfg, err := config.Load("kapsora-migrate")
	if err != nil {
		return err
	}

	switch args[0] {
	case "up":
		st, err := dbmigrate.Up(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		fmt.Printf("schema version %d (dirty=%t)\n", st.Version, st.Dirty)
	case "version":
		st, err := dbmigrate.Version(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		fmt.Printf("schema version %d (dirty=%t)\n", st.Version, st.Dirty)
	case "drop":
		if cfg.IsProductionLike() {
			return fmt.Errorf("drop is refused in environment %q", cfg.Environment)
		}
		if err := dbmigrate.Drop(cfg.DatabaseURL); err != nil {
			return err
		}
		fmt.Println("all objects dropped")
	default:
		return fmt.Errorf("unknown command %q; use up, version or drop", args[0])
	}
	return nil
}
