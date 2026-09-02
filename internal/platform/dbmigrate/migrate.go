// Package dbmigrate applies the embedded forward-only migrations with golang-migrate.
package dbmigrate

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5 driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/celikbros/kapsora/db/migrations"
)

// Status describes the schema after an operation.
type Status struct {
	Version uint
	Dirty   bool
}

// Up applies every pending migration and returns the resulting version.
func Up(databaseURL string) (Status, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return Status{}, err
	}
	defer closeMigrator(m)

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return Status{}, fmt.Errorf("apply migrations: %w", err)
	}
	return status(m)
}

// Version reports the current schema version without changing anything.
func Version(databaseURL string) (Status, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return Status{}, err
	}
	defer closeMigrator(m)
	return status(m)
}

// Drop removes every object in the database. Only for local/test databases.
func Drop(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)
	if err := m.Drop(); err != nil {
		return fmt.Errorf("drop database objects: %w", err)
	}
	return nil
}

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	dbURL, err := pgx5URL(databaseURL)
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return m, nil
}

func status(m *migrate.Migrate) (Status, error) {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return Status{Version: 0, Dirty: false}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("read migration version: %w", err)
	}
	return Status{Version: v, Dirty: dirty}, nil
}

func closeMigrator(m *migrate.Migrate) {
	_, _ = m.Close()
}

// pgx5URL rewrites a postgres:// URL to the pgx5:// scheme golang-migrate expects.
func pgx5URL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql", "pgx5":
		u.Scheme = "pgx5"
	default:
		return "", fmt.Errorf("unsupported database url scheme %q", u.Scheme)
	}
	return u.String(), nil
}
