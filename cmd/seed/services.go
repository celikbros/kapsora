package main

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefiteligibility "github.com/celikbros/kapsora/internal/benefit/eligibility"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	benefitledger "github.com/celikbros/kapsora/internal/benefit/ledger"
	billingapp "github.com/celikbros/kapsora/internal/billing/application"
	billinggw "github.com/celikbros/kapsora/internal/billing/infrastructure/gateway"
	billingpg "github.com/celikbros/kapsora/internal/billing/infrastructure/postgres"
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	claimgw "github.com/celikbros/kapsora/internal/claim/infrastructure/gateway"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthgw "github.com/celikbros/kapsora/internal/health/infrastructure/gateway"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	orgapp "github.com/celikbros/kapsora/internal/organization/application"
	organizationpg "github.com/celikbros/kapsora/internal/organization/infrastructure/postgres"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	pricingapp "github.com/celikbros/kapsora/internal/pricing/application"
	pricingpg "github.com/celikbros/kapsora/internal/pricing/infrastructure/postgres"
	providerapp "github.com/celikbros/kapsora/internal/provider/application"
	providerpg "github.com/celikbros/kapsora/internal/provider/infrastructure/postgres"
	reportapp "github.com/celikbros/kapsora/internal/report/application"
	reportgw "github.com/celikbros/kapsora/internal/report/infrastructure/gateway"
	reportpg "github.com/celikbros/kapsora/internal/report/infrastructure/postgres"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
)

// The verticals `seed demo` drives to build the business scenario of WP-I7 (WP-I7-01..06).
//
// Every one of them is the same service the HTTP handler calls, wired the same way
// cmd/api wires it. That is the whole point of this file: a demo database is only worth
// having if every row in it went through the check that would have refused it in
// production, so the seed owns no repository, no SQL and no shortcut of its own — it owns
// a list of commands and the order to give them in.
type verticals struct {
	organizations *orgapp.Service
	providers     *providerapp.Service
	contracts     *contractapp.Service
	requests      *servicerequestapp.Service
	health        *healthapp.Service
	claims        *claimapp.Service
	billing       *billingapp.Service
	accommodation *accommodationapp.Service
	reports       *reportapp.Service
	documents     *documentapp.Service
	entitlements  *benefitledger.Service
	workflows     *workflowapp.Service
}

// seedDeps are what a seeder is built from. The object store is a parameter rather than
// something this file reads out of the environment, so the tests can hand it the in-memory
// implementation of the same port MinIO implements and drive the whole scenario without one.
type seedDeps struct {
	Pool    *pgxpool.Pool
	Keys    *localkey.Provider
	Store   objectstore.Store
	Storage documentapp.Storage
	Logger  *slog.Logger
	// Clock is the seed's own clock. Every service below that stamps a date is given it, so
	// the demo world can be dated the way a few weeks of business would be rather than all at
	// one instant — an icmal decided a month ago has a settlement that falls due today.
	Clock *seedClock
}

// cursorKey is the codec key the seed signs its own paging with. It is a local development
// constant on purpose: nothing here signs a cursor a user ever holds.
var cursorKey = []byte("kapsora-seed-cursor-key-0123456789ab")

// newVerticals builds every business service the scenario drives.
//
// It reads as one long function because it is one long wiring list, and splitting it would
// hide the one thing worth seeing in it: which port each service is given and which it is
// deliberately left without.
//
//nolint:funlen // one linear wiring list reads better whole, as it does in cmd/api
func newVerticals(d seedDeps) (*verticals, error) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cursors, err := httpx.NewCursorCodec(cursorKey)
	if err != nil {
		return nil, err
	}
	clock := d.Clock
	if clock == nil {
		clock = &seedClock{at: time.Now().UTC()}
	}
	now := clock.now
	out := &verticals{}

	if out.organizations, err = orgapp.New(orgapp.Deps{
		Pool: d.Pool, Repo: organizationpg.New(), Cipher: d.Keys, Index: d.Keys,
		Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, err
	}
	if out.providers, err = providerapp.New(providerapp.Deps{
		Pool: d.Pool, Repo: providerpg.New(), Cipher: d.Keys, Index: d.Keys,
		Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, err
	}
	if out.contracts, err = contractapp.New(contractapp.Deps{
		Pool: d.Pool, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, err
	}
	if out.entitlements, err = benefitledger.New(benefitledger.Deps{
		Pool: d.Pool, Audit: auditpg.New(), Cursors: cursors, Logger: logger,
	}); err != nil {
		return nil, err
	}

	// The rule program cache the quote and the request share, exactly as cmd/api shares it:
	// a published version compiled once is the same program whoever evaluates it.
	programs := rulesapp.NewProgramCache(rulesapp.DefaultCacheSize)
	pricing, err := pricingapp.New(pricingapp.Deps{
		Pool: d.Pool, Repo: pricingpg.New(), Audit: auditpg.New(),
		Programs: programs, Logger: logger, Now: now,
	})
	if err != nil {
		return nil, err
	}
	if out.requests, err = servicerequestapp.New(servicerequestapp.Deps{
		Pool: d.Pool, Repo: servicerequestpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: programs, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	if out.health, err = healthapp.New(healthapp.Deps{
		Pool: d.Pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		StayRepo: healthpg.NewStays(),
		Requests: healthgw.NewRequests(out.requests),
		// No authorization port: the seed opens no stay, and a bug that tried to take a
		// hold here would refuse rather than quietly working.
		WorkItems: healthpg.NewWorkItems(logger), Audit: auditpg.New(),
		Cursors: cursors, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	if out.workflows, err = workflowapp.New(workflowapp.Deps{
		Pool: d.Pool, Repo: workflowpg.New(), Audit: auditpg.New(), Cursors: cursors,
		ClaimHook: out.health, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	if out.claims, err = claimapp.New(claimapp.Deps{
		Pool: d.Pool, Repo: claimpg.New(),
		Pricing:   claimgw.NewPricing(pricing),
		Rules:     claimgw.NewRules(logger),
		Reports:   out.health,
		Policies:  claimgw.NewPolicies(out.workflows),
		WorkItems: claimgw.NewWorkItems(logger),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	if out.billing, err = billingapp.New(billingapp.Deps{
		Pool: d.Pool, Repo: billingpg.New(), Batches: billingpg.NewBatchRepository(),
		Settlements:  billingpg.NewSettlementRepository(),
		Claims:       billinggw.NewClaims(out.claims),
		WorkItems:    billingpg.NewWorkItems(logger),
		Entitlements: billinggw.NewEntitlements(out.entitlements.Ledger()),
		Payments:     billingapp.RecordingPaymentOrders{},
		Cipher:       d.Keys,
		Audit:        auditpg.New(), Cursors: cursors, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	if out.documents, err = documentapp.New(documentapp.Deps{
		Pool: d.Pool, Repo: documentpg.New(), Store: d.Store, Audit: auditpg.New(),
		Cursors: cursors, Logger: logger, Storage: d.Storage, Now: now,
	}); err != nil {
		return nil, err
	}
	// The reporting sweeps. The work item port is the scheduler's, and the seed is given it
	// for the same reason the scheduler is: a reconciliation run that found a difference and
	// raised nothing would be a difference nobody ever sees.
	if out.reports, err = reportapp.New(reportapp.Deps{
		Pool: d.Pool, Repo: reportpg.New(),
		Documents: reportgw.NewDocuments(out.documents),
		WorkItems: reportpg.NewWorkItems(logger),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	eligibility, err := benefiteligibility.New(benefiteligibility.Deps{
		Pool: d.Pool, Audit: auditpg.New(), Ledger: out.entitlements.Ledger(), Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	if out.accommodation, err = accommodationapp.New(accommodationapp.Deps{
		Pool: d.Pool, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Ledger:      out.entitlements.Ledger(),
		Eligibility: eligibility,
		Requests:    accommodationgw.NewRequests(out.requests),
		Policies:    accommodationgw.NewPolicies(out.contracts),
		WorkItems:   accommodationpg.NewWorkItems(logger),
		Audit:       auditpg.New(), Cursors: cursors, Logger: logger, Now: now,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// newReferenceServices builds the four services the reference-data steps of M5/M6 use. It is
// separate from newVerticals because those steps run for both demo tenants and need nothing
// the business scenario needs.
func newReferenceServices(d seedDeps) (catalog *catalogapp.Service,
	notifications *notificationapp.Service, benefits *benefitapp.Service,
	party *partyapp.Service, err error,
) {
	cursors, cerr := httpx.NewCursorCodec(cursorKey)
	if cerr != nil {
		return nil, nil, nil, nil, cerr
	}
	if catalog, err = catalogapp.New(catalogapp.Deps{
		Pool: d.Pool, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, nil, nil, nil, err
	}
	// No channel adapters: the seed publishes templates and sends nothing.
	if notifications, err = notificationapp.New(notificationapp.Deps{
		Pool: d.Pool, Repo: notificationpg.New(nil), Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, nil, nil, nil, err
	}
	if benefits, err = benefitapp.New(benefitapp.Deps{
		Pool: d.Pool, Repo: benefitpg.New(), Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, nil, nil, nil, err
	}
	if party, err = partyapp.New(partyapp.Deps{
		Pool: d.Pool, Repo: partypg.New(), Cipher: d.Keys, Index: d.Keys,
		Audit: auditpg.New(), Cursors: cursors,
	}); err != nil {
		return nil, nil, nil, nil, err
	}
	return catalog, notifications, benefits, party, nil
}
