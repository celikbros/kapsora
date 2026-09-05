// seed creates local development data. It refuses to run against a production-like
// environment and is idempotent: running it twice leaves one set of data.
//
//	seed account <username> <display name> [email]   one login with a random password
//	seed demo                                        tenants DEMO_A / DEMO_B, demo users, grants
//
// Generated passwords are printed once and never stored in plaintext. Set
// KAPSORA_SEED_DEMO_PASSWORD to give every demo user the same known password (local only).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

type seeder struct {
	pool        *pgxpool.Pool
	svc         *application.Service
	credentials *identitypg.CredentialRepository
	provisioner *application.Provisioner
	// The three services the reference data of WP-I5-05 goes through. The seed writes
	// them the way an operator would rather than with its own INSERTs, so a template that
	// the publishing gate would refuse is refused here too.
	catalog       *catalogapp.Service
	notifications *notificationapp.Service
	benefits      *benefitapp.Service
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: seed account <username> <display name> [email] | seed demo")
	}
	cfg, err := config.Load("kapsora-seed")
	if err != nil {
		return err
	}
	if cfg.IsProductionLike() {
		return fmt.Errorf("seed is refused in environment %q", cfg.Environment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.PoolOptions{ApplicationName: "kapsora-seed", MaxConns: 2})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Seed actions leave the same audit trail as an administrator would (tenant.provision,
	// access_grant.create); the actor column stays empty because nobody is signed in.
	sink := identitypg.NewAuditSink(pool, auditpg.New(), slog.Default())
	credentials := identitypg.NewCredentialRepository(pool)
	svc, err := application.New(application.Deps{
		Credentials: credentials,
		Sessions:    identitypg.NewSessionStore(pool),
		Audit:       sink,
		Policy:      domain.DefaultPolicy(),
		Lockout:     domain.DefaultLockout(),
	})
	if err != nil {
		return err
	}
	// The seed answers no paged HTTP request, but the catalogue and notification services
	// require a cursor codec for the list reads they use to stay idempotent. The key is a
	// local development constant on purpose: nothing signs a cursor a user ever holds.
	cursors, err := httpx.NewCursorCodec([]byte("kapsora-seed-cursor-key-0123456789ab"))
	if err != nil {
		return err
	}
	catalogSvc, err := catalogapp.New(catalogapp.Deps{
		Pool: pool, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	// No channel adapters: the seed publishes templates and sends nothing.
	notificationSvc, err := notificationapp.New(notificationapp.Deps{
		Pool: pool, Repo: notificationpg.New(nil), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	benefitSvc, err := benefitapp.New(benefitapp.Deps{
		Pool: pool, Repo: benefitpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	s := &seeder{
		pool:          pool,
		svc:           svc,
		credentials:   credentials,
		provisioner:   application.NewProvisioner(identitypg.NewProvisioningRepository(pool), sink),
		catalog:       catalogSvc,
		notifications: notificationSvc,
		benefits:      benefitSvc,
	}

	switch args[0] {
	case "account":
		if len(args) < 3 {
			return fmt.Errorf("usage: seed account <username> <display name> [email]")
		}
		email := ""
		if len(args) > 3 {
			email = args[3]
		}
		_, err := s.ensureAccount(ctx, args[1], args[2], email, "")
		return err
	case "demo":
		return s.demo(ctx)
	default:
		return fmt.Errorf("unknown command %q; use account or demo", args[0])
	}
}

// ensureAccount creates the login when it does not exist and prints the password once.
// fixedPassword may be empty for a random one.
func (s *seeder) ensureAccount(ctx context.Context, username, displayName, email, fixedPassword string) (uuid.UUID, error) {
	existing, err := s.credentials.FindByUsername(ctx, domain.NormalizeUsername(username))
	if err == nil {
		fmt.Printf("account %-22s exists (actor %s)\n", existing.Username, existing.ActorID)
		return existing.ActorID, nil
	}
	if !errors.Is(err, application.ErrAccountNotFound) {
		return uuid.Nil, err
	}
	password := fixedPassword
	if password == "" {
		if password, err = randomPassword(); err != nil {
			return uuid.Nil, err
		}
	}
	actorID, err := s.svc.CreateAccount(ctx, username, displayName, email, password, fixedPassword == "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("create account %s: %w", username, err)
	}
	if fixedPassword == "" {
		fmt.Printf("account %-22s created (actor %s)\n  password: %s  (shown only now; must be changed on first use)\n",
			domain.NormalizeUsername(username), actorID, password)
	} else {
		fmt.Printf("account %-22s created (actor %s) with KAPSORA_SEED_DEMO_PASSWORD\n", domain.NormalizeUsername(username), actorID)
	}
	return actorID, nil
}

func (s *seeder) ensureTenant(ctx context.Context, code, legalName, displayName string) (uuid.UUID, error) {
	id, err := s.provisioner.ProvisionTenant(ctx, application.ProvisionCommand{Code: code, LegalName: legalName, DisplayName: displayName})
	if err == nil {
		fmt.Printf("tenant  %-22s provisioned (%s)\n", code, id)
		return id, nil
	}
	if !errors.Is(err, application.ErrTenantCodeExists) {
		return uuid.Nil, err
	}
	id, err = sqlcgen.New(s.pool).GetTenantIDByCode(ctx, code)
	if err != nil {
		return uuid.Nil, fmt.Errorf("look up tenant %s: %w", code, err)
	}
	fmt.Printf("tenant  %-22s exists (%s)\n", code, id)
	return id, nil
}

// demo builds the M1 demo data set (WP-I1-02 section 6, adapted to local accounts).
func (s *seeder) demo(ctx context.Context) error {
	demoPassword := os.Getenv("KAPSORA_SEED_DEMO_PASSWORD")

	tenantA, err := s.ensureTenant(ctx, "DEMO_A", "Demo Kurum A A.Ş.", "Demo Kurum A")
	if err != nil {
		return err
	}
	tenantB, err := s.ensureTenant(ctx, "DEMO_B", "Demo Kurum B A.Ş.", "Demo Kurum B")
	if err != nil {
		return err
	}
	hospital, err := s.provisioner.EnsureProviderOrganization(ctx, tenantA, "DEMO_HOSPITAL", "Demo Hastane")
	if err != nil {
		return err
	}

	users := []struct{ username, name string }{
		{"admin.a", "Ayşe Admin"},
		{"reviewer.a", "Rıza Değerlendirici"},
		{"provider.a", "Pınar Sağlayıcı"},
		{"admin.b", "Burak Admin"},
		{"both.ab", "Deniz Denetçi"},
	}
	actors := map[string]uuid.UUID{}
	for _, u := range users {
		id, err := s.ensureAccount(ctx, u.username, u.name, u.username+"@demo.test", demoPassword)
		if err != nil {
			return err
		}
		actors[u.username] = id
	}

	grants := []application.GrantRoleInput{
		{TenantID: tenantA, ActorID: actors["admin.a"], RoleCode: "TENANT_ADMIN"},
		{TenantID: tenantA, ActorID: actors["admin.a"], RoleCode: "PROGRAM_MANAGER"},
		{TenantID: tenantA, ActorID: actors["reviewer.a"], RoleCode: "FINANCIAL_REVIEWER"},
		{TenantID: tenantA, ActorID: actors["provider.a"], RoleCode: "PROVIDER_STAFF",
			ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true}},
		{TenantID: tenantB, ActorID: actors["admin.b"], RoleCode: "TENANT_ADMIN"},
		{TenantID: tenantA, ActorID: actors["both.ab"], RoleCode: "AUDITOR"},
		{TenantID: tenantB, ActorID: actors["both.ab"], RoleCode: "AUDITOR"},
	}
	for _, g := range grants {
		g.Reason = "seed demo"
		created, err := s.provisioner.GrantRole(ctx, g)
		if err != nil {
			return fmt.Errorf("grant %s: %w", g.RoleCode, err)
		}
		state := "exists"
		if created {
			state = "granted"
		}
		fmt.Printf("grant   %-22s %s in %s\n", g.RoleCode, state, shortTenant(g.TenantID, tenantA))
	}
	// Reference data. Each of these is idempotent and each is per tenant: a template
	// belongs to a tenant, a code system belongs to a tenant, and a mapping belongs to one
	// of a tenant's plan versions.
	for _, tenantID := range []uuid.UUID{tenantA, tenantB} {
		if err := s.ensureTemplates(ctx, tenantID); err != nil {
			return err
		}
		if err := s.ensureICD10(ctx, tenantID); err != nil {
			return err
		}
		if err := s.ensureEntitlementMappings(ctx, tenantID); err != nil {
			return err
		}
	}
	fmt.Println("demo data ready")
	return nil
}

func shortTenant(id, a uuid.UUID) string {
	if id == a {
		return "DEMO_A"
	}
	return "DEMO_B"
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
