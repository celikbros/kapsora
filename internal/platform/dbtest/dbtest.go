// Package dbtest provisions throw-away PostgreSQL 18 databases for integration tests.
//
// It needs an administrative connection URL in KAPSORA_TEST_ADMIN_DATABASE_URL (a role
// that can CREATE DATABASE and CREATE ROLE: the postgres superuser locally, the service
// container in CI). New creates a database named kapsora_test_<random>, ensures the
// non-privileged kapsora_app role exists, applies every embedded migration as the admin
// and hands back two pools: Admin (schema owner, bypasses RLS) for setup and App
// (kapsora_app, subject to RLS) for the behaviour under test. Without the variable the
// calling test skips, so unit-only runs stay green on machines without PostgreSQL.
package dbtest

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
	// AdminURLEnv names the administrative connection URL variable.
	AdminURLEnv = "KAPSORA_TEST_ADMIN_DATABASE_URL"
	// AppPasswordEnv overrides the password set on the application role.
	AppPasswordEnv = "KAPSORA_TEST_APP_PASSWORD" //nolint:gosec // environment variable name, not a credential
	// AppRole is the non-privileged application role name.
	AppRole = "kapsora_app"

	// defaultAppPassword is a throw-away local test value matching .env.example; the
	// test databases it protects are created and dropped by the harness itself.
	defaultAppPassword = "kapsora_app_local" //nolint:gosec // local test default, never used outside throw-away databases
)

// SQLSTATE codes used in assertions.
const (
	SQLStateInsufficientPrivilege = "42501" // RLS WITH CHECK violation, missing grant
	SQLStateForeignKeyViolation   = "23503"
	SQLStateUniqueViolation       = "23505"
	SQLStateCheckViolation        = "23514"
	SQLStateExclusionViolation    = "23P01"
	SQLStateIntegrityConstraint   = "23000" // raised by the guard/append-only triggers
)

// Harness bundles the admin and application pools of one throw-away database.
type Harness struct {
	T         *testing.T
	Admin     *pgxpool.Pool
	App       *pgxpool.Pool
	DBName    string
	AdminURL  string
	AppURL    string
	Migration dbmigrate.Status
}

// New creates the database, the app role and applies migrations; cleanup drops it.
func New(t *testing.T) *Harness {
	t.Helper()
	baseURL := os.Getenv(AdminURLEnv)
	if baseURL == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration tests", AdminURLEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	maint, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("dbtest: connect maintenance db: %v", err)
	}

	dbName := "kapsora_test_" + randomHex(4)
	if _, err := maint.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}
	ensureAppRole(ctx, t, maint)
	if err := maint.Close(ctx); err != nil {
		t.Fatalf("dbtest: close maintenance connection: %v", err)
	}

	adminURL := withDatabase(t, baseURL, dbName)
	appURL := withUser(t, adminURL, AppRole, appPassword())

	st, err := dbmigrate.Up(adminURL)
	if err != nil {
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("dbtest: migrate up: %v", err)
	}

	admin, err := db.NewPool(ctx, adminURL, db.PoolOptions{ApplicationName: "dbtest-admin", MaxConns: 4})
	if err != nil {
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("dbtest: admin pool: %v", err)
	}
	app, err := db.NewPool(ctx, appURL, db.PoolOptions{ApplicationName: "dbtest-app", MaxConns: 4})
	if err != nil {
		admin.Close()
		dropDatabase(t, baseURL, dbName)
		t.Fatalf("dbtest: app pool: %v", err)
	}

	h := &Harness{T: t, Admin: admin, App: app, DBName: dbName, AdminURL: adminURL, AppURL: appURL, Migration: st}
	t.Cleanup(func() {
		app.Close()
		admin.Close()
		dropDatabase(t, baseURL, dbName)
	})
	return h
}

// Ctx returns a 30 second context for one statement or transaction.
func (h *Harness) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// AdminExec runs a statement as the schema owner and fails the test on error.
func (h *Harness) AdminExec(sql string, args ...any) {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	if _, err := h.Admin.Exec(ctx, sql, args...); err != nil {
		h.T.Fatalf("admin exec failed: %v\nsql: %s", err, sql)
	}
}

// AdminExecErr runs a statement as the owner and returns the error for assertions.
func (h *Harness) AdminExecErr(sql string, args ...any) error {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	_, err := h.Admin.Exec(ctx, sql, args...)
	return err
}

// AppTx runs fn as kapsora_app inside a tenant-bound transaction.
func (h *Harness) AppTx(tenantID uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := h.Ctx()
	defer cancel()
	return db.WithTenantTx(ctx, h.App, db.TenantContext{TenantID: tenantID}, fn)
}

// AppActorTx runs fn as kapsora_app with only the actor bound (pre-tenant phase).
func (h *Harness) AppActorTx(actorID uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := h.Ctx()
	defer cancel()
	return db.WithActorTx(ctx, h.App, actorID, fn)
}

// CreateTenant inserts a tenant as the owner and returns its id.
func (h *Harness) CreateTenant(code string) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx,
		`INSERT INTO platform.tenant (code, legal_name, display_name) VALUES ($1, $2, $2) RETURNING id`,
		code, "Tenant "+code).Scan(&id)
	if err != nil {
		h.T.Fatalf("create tenant %s: %v", code, err)
	}
	return id
}

// CreateTenantOrganization creates a global organization and its tenant relationship,
// returning the relationship (directory.tenant_organization) id.
func (h *Harness) CreateTenantOrganization(tenantID uuid.UUID, name, role string) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var orgID, relID uuid.UUID
	err := h.Admin.QueryRow(ctx,
		`INSERT INTO directory.organization (legal_name, display_name, organization_kind)
		 VALUES ($1, $1, 'OTHER') RETURNING id`, name).Scan(&orgID)
	if err != nil {
		h.T.Fatalf("create organization: %v", err)
	}
	err = h.Admin.QueryRow(ctx,
		`INSERT INTO directory.tenant_organization (tenant_id, organization_id, relationship_role)
		 VALUES ($1, $2, $3) RETURNING id`, tenantID, orgID, role).Scan(&relID)
	if err != nil {
		h.T.Fatalf("create tenant organization: %v", err)
	}
	return relID
}

// CreateActor inserts an iam.actor row and returns its id.
func (h *Harness) CreateActor(subject, displayName string) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx,
		`INSERT INTO iam.actor (identity_subject, identity_issuer, actor_type, display_name)
		 VALUES ($1, 'https://test.issuer.local/realms/kapsora', 'HUMAN', $2) RETURNING id`,
		subject, displayName).Scan(&id)
	if err != nil {
		h.T.Fatalf("create actor: %v", err)
	}
	return id
}

// CreateMembership adds an active tenant membership for the actor.
func (h *Harness) CreateMembership(tenantID, actorID uuid.UUID) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx,
		`INSERT INTO iam.tenant_membership (tenant_id, actor_id) VALUES ($1, $2) RETURNING id`,
		tenantID, actorID).Scan(&id)
	if err != nil {
		h.T.Fatalf("create membership: %v", err)
	}
	return id
}

// SQLState extracts the SQLSTATE of a PostgreSQL error, or "" for other errors.
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// ExpectSQLState fails unless err carries the given SQLSTATE.
func ExpectSQLState(t *testing.T, err error, want string, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected SQLSTATE %s, got success", what, want)
	}
	if got := SQLState(err); got != want {
		t.Fatalf("%s: expected SQLSTATE %s, got %q (%v)", what, want, got, err)
	}
}

func appPassword() string {
	if v := os.Getenv(AppPasswordEnv); v != "" {
		return v
	}
	return defaultAppPassword
}

func ensureAppRole(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(ctx, fmt.Sprintf(`
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
        CREATE ROLE %[1]s LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END $$;`, AppRole))
	if err != nil {
		t.Fatalf("dbtest: create app role: %v", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s WITH LOGIN PASSWORD '%s'`, AppRole, appPassword())); err != nil {
		t.Fatalf("dbtest: set app role password: %v", err)
	}
}

func dropDatabase(t *testing.T, baseURL, dbName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Logf("dbtest cleanup: connect maintenance db: %v", err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Logf("dbtest cleanup: drop database %s: %v", dbName, err)
	}
}

func withDatabase(t *testing.T, rawURL, dbName string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("dbtest: parse %s: %v", AdminURLEnv, err)
	}
	u.Path = "/" + dbName
	return u.String()
}

func withUser(t *testing.T, rawURL, user, password string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("dbtest: parse url: %v", err)
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
