// Package dbtests runs the schema against a real PostgreSQL 18 (v1.2 section 44.1).
//
// It needs an administrative connection URL in KAPSORA_TEST_ADMIN_DATABASE_URL (a role
// that can CREATE DATABASE and CREATE ROLE, e.g. the postgres superuser on a local
// instance or the CI service container). Each test run creates a throw-away database,
// creates the non-privileged kapsora_app role, applies every migration as the admin and
// then exercises the schema through both roles. Without the variable the tests skip.
package dbtests

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
)

const (
	adminURLEnv    = "KAPSORA_TEST_ADMIN_DATABASE_URL"
	appPasswordEnv = "KAPSORA_TEST_APP_PASSWORD"
	appRole        = "kapsora_app"
	// Same default as docker-compose.yml and .env.example so a developer database and the
	// test role share one local password; override with KAPSORA_TEST_APP_PASSWORD.
	defaultAppPassword = "kapsora_app_local"
)

// appPassword is the password the harness sets on the application role.
var appPassword = func() string {
	if v := os.Getenv(appPasswordEnv); v != "" {
		return v
	}
	return defaultAppPassword
}()

// harness bundles the admin and application pools for one throw-away database.
type harness struct {
	t         *testing.T
	admin     *pgxpool.Pool // owner: bypasses RLS (superuser) - used for setup only
	app       *pgxpool.Pool // kapsora_app: subject to RLS, no bypass
	dbName    string
	adminURL  string
	appURL    string
	migration dbmigrate.Status
}

// newHarness creates the database, the app role and applies migrations.
func newHarness(t *testing.T) *harness {
	t.Helper()
	baseURL := os.Getenv(adminURLEnv)
	if baseURL == "" {
		t.Skipf("%s not set; skipping PostgreSQL schema tests", adminURLEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	maint, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect maintenance db: %v", err)
	}

	dbName := "kapsora_test_" + randomHex(4)
	if _, err := maint.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	ensureAppRole(ctx, t, maint)
	if err := maint.Close(ctx); err != nil {
		t.Fatalf("close maintenance connection: %v", err)
	}

	adminURL := withDatabase(t, baseURL, dbName)
	appURL := withUser(t, adminURL, appRole, appPassword)

	st, err := dbmigrate.Up(adminURL)
	if err != nil {
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("migrate up: %v", err)
	}

	admin, err := db.NewPool(ctx, adminURL, db.PoolOptions{ApplicationName: "dbtests-admin", MaxConns: 4})
	if err != nil {
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("admin pool: %v", err)
	}
	app, err := db.NewPool(ctx, appURL, db.PoolOptions{ApplicationName: "dbtests-app", MaxConns: 4})
	if err != nil {
		admin.Close()
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("app pool: %v", err)
	}

	h := &harness{t: t, admin: admin, app: app, dbName: dbName, adminURL: adminURL, appURL: appURL, migration: st}
	t.Cleanup(func() {
		app.Close()
		admin.Close()
		dropDatabase(t, baseURL, dbName)
	})
	return h
}

func ensureAppRole(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(ctx, fmt.Sprintf(`
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
        CREATE ROLE %[1]s LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END $$;`, appRole))
	if err != nil {
		t.Fatalf("create app role: %v", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s WITH LOGIN PASSWORD '%s'`, appRole, appPassword)); err != nil {
		t.Fatalf("set app role password: %v", err)
	}
}

func dropDatabase(t *testing.T, baseURL, dbName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Logf("cleanup: connect maintenance db: %v", err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Logf("cleanup: drop database %s: %v", dbName, err)
	}
}

func withDatabase(t *testing.T, rawURL, dbName string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", adminURLEnv, err)
	}
	u.Path = "/" + dbName
	return u.String()
}

func withUser(t *testing.T, rawURL, user, password string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---- helpers used by the test files ----

func (h *harness) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// adminExec runs a statement as the schema owner (bypasses RLS).
func (h *harness) adminExec(sql string, args ...any) {
	h.t.Helper()
	ctx, cancel := h.ctx()
	defer cancel()
	if _, err := h.admin.Exec(ctx, sql, args...); err != nil {
		h.t.Fatalf("admin exec failed: %v\nsql: %s", err, sql)
	}
}

// adminExecErr runs a statement as the owner and returns the error for assertions.
func (h *harness) adminExecErr(sql string, args ...any) error {
	h.t.Helper()
	ctx, cancel := h.ctx()
	defer cancel()
	_, err := h.admin.Exec(ctx, sql, args...)
	return err
}

// appTx runs fn as kapsora_app inside a tenant-bound transaction.
func (h *harness) appTx(tenantID uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := h.ctx()
	defer cancel()
	return db.WithTenantTx(ctx, h.app, db.TenantContext{TenantID: tenantID}, fn)
}

// createTenant inserts a tenant as the owner and returns its id.
func (h *harness) createTenant(code string) uuid.UUID {
	h.t.Helper()
	ctx, cancel := h.ctx()
	defer cancel()
	var id uuid.UUID
	err := h.admin.QueryRow(ctx,
		`INSERT INTO platform.tenant (code, legal_name, display_name) VALUES ($1, $2, $2) RETURNING id`,
		code, "Tenant "+code).Scan(&id)
	if err != nil {
		h.t.Fatalf("create tenant %s: %v", code, err)
	}
	return id
}

// createTenantOrganization creates a global organization and its tenant relationship.
func (h *harness) createTenantOrganization(tenantID uuid.UUID, name, role string) uuid.UUID {
	h.t.Helper()
	ctx, cancel := h.ctx()
	defer cancel()
	var orgID, relID uuid.UUID
	err := h.admin.QueryRow(ctx,
		`INSERT INTO directory.organization (legal_name, display_name, organization_kind)
		 VALUES ($1, $1, 'OTHER') RETURNING id`, name).Scan(&orgID)
	if err != nil {
		h.t.Fatalf("create organization: %v", err)
	}
	err = h.admin.QueryRow(ctx,
		`INSERT INTO directory.tenant_organization (tenant_id, organization_id, relationship_role)
		 VALUES ($1, $2, $3) RETURNING id`, tenantID, orgID, role).Scan(&relID)
	if err != nil {
		h.t.Fatalf("create tenant organization: %v", err)
	}
	return relID
}

// sqlState extracts the SQLSTATE of a PostgreSQL error, or "" for other errors.
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// expectSQLState fails unless err carries the given SQLSTATE.
func expectSQLState(t *testing.T, err error, want string, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected SQLSTATE %s, got success", what, want)
	}
	if got := sqlState(err); got != want {
		t.Fatalf("%s: expected SQLSTATE %s, got %q (%v)", what, want, got, err)
	}
}

// SQLSTATE codes used in assertions.
const (
	stateInsufficientPrivilege     = "42501" // RLS WITH CHECK violation
	stateForeignKeyViolation       = "23503"
	stateUniqueViolation           = "23505"
	stateCheckViolation            = "23514"
	stateExclusionViolation        = "23P01"
	stateIntegrityConstraintRaised = "23000" // raised by our guard triggers
)
