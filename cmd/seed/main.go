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
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// newSeedObjectStore builds the S3 client the document steps of the business scenario write
// through. It refuses rather than falling back to something that stores nothing: a document
// row whose bytes were never written is a download that answers 500 six weeks later, and the
// seed is the one place where saying so immediately costs nobody anything.
func newSeedObjectStore(cfg config.Config) (objectstore.Store, error) {
	if !cfg.Documents.Configured() {
		return nil, fmt.Errorf(
			"seed demo needs an object store for the invoice scan and the member's receipt; " +
				"set KAPSORA_MINIO_ROOT_USER and KAPSORA_MINIO_ROOT_PASSWORD (see .env.example) " +
				"and start it with scripts/native/up")
	}
	return objectstore.NewS3(objectstore.S3Options{
		Endpoint: cfg.Documents.Endpoint, Region: cfg.Documents.Region,
		AccessKey: cfg.Documents.AccessKey, SecretKey: cfg.Documents.SecretKey,
	})
}

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
	// party creates the demo principal a member account is bound to (WP-I6-04). It needs
	// the field cipher and the blind indexer, because a person without an identifier is
	// not a person anybody could be found as.
	party *partyapp.Service
	// keys is the platform cipher and blind indexer. The seed holds it because a VKN and a
	// TCKN reach the database as an envelope or they do not reach it at all.
	keys *localkey.Provider
	// biz are the verticals the WP-I7 business scenario is driven through. It is nil in a
	// seeder built for the reference-data steps alone.
	biz *verticals
	// nowFn is what the business scenario reads as "today"; nil means the wall clock. A test
	// moves it to prove that a second run on another day finds what the first one opened
	// instead of opening a second icmal for the same invoice.
	nowFn func() time.Time
	// clock is what every dated row the business scenario writes is stamped with. The steps
	// move it, so a demo database reads like a few weeks of business rather than one instant.
	clock *seedClock
	// claimEpoch and claimSeq give every claim of one run a service date of its own, so the
	// demo world does not trip WP-I5-04's duplicate check on itself.
	claimEpoch time.Time
	claimSeq   int
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	// The same local key provider cmd/api uses (ADR-020). The seed writes an identifier
	// through the ordinary encryption path rather than an INSERT of its own, so a demo
	// database holds a TCKN exactly the way a real one does: in person_identifier.value_enc
	// and nowhere else.
	keys, err := localkey.NewFromEnv()
	if err != nil {
		return err
	}
	clock := &seedClock{at: time.Now().UTC()}
	deps := seedDeps{
		Pool: pool, Keys: keys, Logger: slog.Default(), Clock: clock,
		Storage: documentapp.Storage{
			QuarantineBucket: cfg.Documents.QuarantineBucket,
			SecureBucket:     cfg.Documents.SecureBucket,
			UploadTTL:        cfg.Documents.UploadURLTTL,
			DownloadTTL:      cfg.Documents.DownloadURLTTL,
			EncryptionKeyRef: cfg.Documents.EncryptionKeyRef,
		},
	}
	catalogSvc, notificationSvc, benefitSvc, partySvc, err := newReferenceServices(deps)
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
		party:         partySvc,
		keys:          keys,
		clock:         clock,
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
		// The object store the business scenario's documents live in: the scan of an
		// invoice and the member's receipt. It is the same S3 port cmd/api signs its upload
		// URLs against, and the seed is refused without one rather than writing a document
		// row with no bytes behind it — a download that answered 500 six weeks later would
		// be worse than a demo that stops now and says what is missing. It is built here
		// rather than above because `seed account` needs no file at all.
		if deps.Store, err = newSeedObjectStore(cfg); err != nil {
			return err
		}
		if s.biz, err = newVerticals(deps); err != nil {
			return err
		}
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
	// A tenant provisioned by an earlier seed has the roles the templates had then. The
	// grants below name roles and lean on permissions added since, so bring it up to date
	// first; on a tenant already in step this writes nothing.
	res, err := s.provisioner.SyncSystemRoles(ctx, id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("sync roles of %s: %w", code, err)
	}
	if res.Changed() {
		fmt.Printf("roles   %-22s %d created %v, %d permissions added\n", code, len(res.RolesCreated), res.RolesCreated, res.PermissionsAdded)
	}
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

	// The demo people. The first five are M1's; the six below them are the ones the six
	// scenarios of scripts/demo/KAPSORA-Demo-Rehberi.html are walked as, and their names are
	// the guide's own so that a reader of the guide and a reader of the account table are
	// looking at the same person.
	users := []struct{ username, name string }{
		{"admin.a", "Ayşe Admin"},
		{"reviewer.a", "Rıza Değerlendirici"},
		{"provider.a", "Pınar Sağlayıcı"},
		{"admin.b", "Burak Admin"},
		{"both.ab", "Deniz Denetçi"},
		{"financial.reviewer", "Fuat Mali Değerlendirici"},
		{"payer.approver", "Pınar Ödeyici Onaylayıcı"},
		{"doctor.a", "Demet Tıbbi Değerlendirici"},
		{"sponsor.hr", "Selin İnsan Kaynakları"},
		{"billing.a", "Burak Faturalama"},
		{"reservation.a", "Rezan Rezervasyon"},
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
		// WP-I7's people. Each holds exactly the role the mock world grants the same
		// username, so the guide and the login work the same way against the mock and
		// against the real API.
		{TenantID: tenantA, ActorID: actors["financial.reviewer"], RoleCode: "FINANCIAL_REVIEWER"},
		// A separate account from the reviewer on purpose: every maker-checker rule in the
		// settlement is a rule about *which person*, and one account holding both would make
		// scenario 3 untestable.
		{TenantID: tenantA, ActorID: actors["payer.approver"], RoleCode: "PAYER_APPROVER"},
		{TenantID: tenantA, ActorID: actors["doctor.a"], RoleCode: "MEDICAL_REVIEWER"},
		{TenantID: tenantA, ActorID: actors["sponsor.hr"], RoleCode: "SPONSOR_HR"},
		// The billing desk and the clinic desk of one hospital see the same organization and
		// neither sees anybody else's, so `billing.a` is scoped to the relationship row
		// `provider.a` is scoped to.
		{TenantID: tenantA, ActorID: actors["billing.a"], RoleCode: "PROVIDER_BILLING",
			ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true}},
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
		if err := s.ensureAccommodationSettings(ctx, tenantID); err != nil {
			return err
		}
	}
	// The member binding is DEMO_A's only: a member account acts for one person in one
	// tenant, and a second binding in DEMO_B would make the mock world ambiguous about
	// which one member.a is.
	memberActor, err := s.ensureDemoMember(ctx, tenantA, demoPassword)
	if err != nil {
		return err
	}
	actors[demoMemberUsername] = memberActor
	// The staff member is a medical reviewer and a member at once: two grants on one account,
	// the second binding her to a person of her own. The server applies whichever the app a
	// request comes from calls for.
	staffActor, err := s.ensureStaffMember(ctx, tenantA, demoPassword)
	if err != nil {
		return err
	}
	actors[staffMemberUsername] = staffActor

	// The business world of WP-I7: the contract, the plan, the claims, the invoices, the
	// icmals, the settlements, the member's reimbursement, the hotel's allotment and the
	// reconciliation runs — every state the six scenarios of the demo guide open on.
	//
	// It runs last because it needs everything above it: the tenant, the accounts, the
	// grants, the notification templates and the bound member.
	if s.biz != nil {
		if err := s.ensureBusinessScenario(ctx, tenantA, actors); err != nil {
			return fmt.Errorf("business scenario: %w", err)
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
