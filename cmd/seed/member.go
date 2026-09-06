package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/settings"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The demo family. One principal is enough to make the member screens of WP-I6-05 real:
// what they need is an account that knows which person it is, and every one of the member
// commands of WP-I6-01..03 resolves that person on the server.
//
// The identifier is a synthetic TCKN that passes the checksum and belongs to nobody: it is
// the same value the notification tests use as their "an identity number" payload, which is
// deliberate — a demo data set seeded with a real number would be a real number in every
// developer's database.
const (
	demoMemberUsername  = "member.a"
	demoMemberFirstName = "Melis"
	demoMemberLastName  = "Üye"
	demoMemberTCKN      = "10000000146"
)

// ensureDemoMember binds a member account to a person in the tenant: the person is created
// if it is not there, the account is created if it is not there, and the MEMBER role is
// granted with a PERSON scope naming that person.
//
// The grant is what the whole of WP-I6-04 section 2.4 rests on, so it is worth being
// precise about who may write one: the seed and M10's onboarding, never a member. A member
// who could create their own PERSON grant could name anybody.
func (s *seeder) ensureDemoMember(ctx context.Context, tenantID uuid.UUID, demoPassword string) error {
	rc := identity.RequestContext{TenantID: tenantID}

	personID, err := s.ensureDemoPerson(ctx, rc)
	if err != nil {
		return err
	}
	actorID, err := s.ensureAccount(ctx, demoMemberUsername, demoMemberFirstName+" "+demoMemberLastName,
		demoMemberUsername+"@demo.test", demoPassword)
	if err != nil {
		return err
	}
	created, err := s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{
		TenantID: tenantID, ActorID: actorID, RoleCode: "MEMBER",
		ScopeType: identityapp.ScopePerson,
		ScopeID:   uuid.NullUUID{UUID: personID, Valid: true},
		Reason:    "seed demo member binding",
	})
	if err != nil {
		return fmt.Errorf("bind %s to a person: %w", demoMemberUsername, err)
	}
	state := "exists"
	if created {
		state = "granted"
	}
	fmt.Printf("member  %-22s %s (person %s)\n", demoMemberUsername, state, personID)
	return nil
}

// ensureDemoPerson finds the demo principal by name or creates it. Finding by name rather
// than by identifier is deliberate: searching by identifier goes through the blind index
// and audits a member identifier search, and a seed run should not leave a trail that looks
// like somebody looking a person up.
func (s *seeder) ensureDemoPerson(ctx context.Context, rc identity.RequestContext) (uuid.UUID, error) {
	page, err := s.party.List(ctx, rc, partyapp.ListFilter{Query: demoMemberLastName, Limit: 50})
	if err != nil {
		return uuid.Nil, fmt.Errorf("look up the demo person: %w", err)
	}
	want := demoMemberFirstName + " " + demoMemberLastName
	for _, item := range page.Items {
		if item.DisplayName == want {
			return item.ID, nil
		}
	}
	person, err := s.party.Create(ctx, rc, domain.NewPerson{
		FirstName: demoMemberFirstName,
		LastName:  demoMemberLastName,
		Identifiers: []domain.SubmittedIdentifier{
			{Type: "TCKN", Value: demoMemberTCKN, Primary: true},
		},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create the demo person: %w", err)
	}
	return person.ID, nil
}

// ensureAccommodationSettings writes the accommodation keys for a demo tenant.
//
// It writes the documented defaults rather than something else, so a developer reading a
// screen sees the same numbers the code would have used with no rows at all -- and can then
// change one row and watch the behaviour move, which is the only reason to seed a setting
// whose default is already right.
func (s *seeder) ensureAccommodationSettings(ctx context.Context, tenantID uuid.UUID) error {
	defaults := settings.Defaults()
	values := map[string]string{
		settings.KeyHoldMinutes:        strconv.Itoa(defaults.HoldMinutes),
		settings.KeyQuoteTTLMinutes:    strconv.Itoa(defaults.QuoteTTLMinutes),
		settings.KeyCheckInEarlyHours:  strconv.Itoa(defaults.CheckInEarlyHours),
		settings.KeyCheckInLateHours:   strconv.Itoa(defaults.CheckInLateHours),
		settings.KeyMaxNights:          strconv.Itoa(defaults.MaxNights),
		settings.KeyStepUpMemberAmount: defaults.StepUpMemberAmount,
	}
	// Inside a tenant transaction, because platform.tenant_setting has RLS forced and the
	// seed connects as the ordinary application role: a write with no bound tenant is
	// refused by the policy exactly as it would be in the request path.
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		return writeAccommodationSettings(ctx, tx, tenantID, values)
	})
}

func writeAccommodationSettings(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, values map[string]string) error {
	q := sqlcgen.New(tx)
	for _, key := range settings.Keys {
		// The threshold is a money value and is stored as a JSON string, not a JSON
		// number: a float in the document would be the one place in this system where an
		// amount could round, and it would round in the place that decides whether
		// somebody is asked for their password.
		var encoded []byte
		var err error
		if key == settings.KeyStepUpMemberAmount {
			encoded, err = json.Marshal(values[key])
		} else {
			encoded, err = json.Marshal(json.RawMessage(values[key]))
		}
		if err != nil {
			return fmt.Errorf("encode setting %s: %w", key, err)
		}
		if err := q.UpsertTenantSetting(ctx, sqlcgen.UpsertTenantSettingParams{
			TenantID: tenantID, SettingKey: key, ValueJson: encoded,
		}); err != nil {
			return fmt.Errorf("write setting %s: %w", key, err)
		}
	}
	fmt.Printf("setting %-22s %d accommodation keys\n", "accommodation", len(settings.Keys))
	return nil
}
