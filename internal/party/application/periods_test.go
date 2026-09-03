package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

func TestRelationshipsCoverBothDirectionsAndRejectOverlaps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	parent := f.create(t, f.tenantA, "Ebeveyn", "Yılmaz")
	child := f.create(t, f.tenantA, "Çocuk", "Yılmaz")

	rel, err := f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: child.ID, RelationshipType: "CHILD", ValidFrom: date(t, "2020-01-01"),
	})
	if err != nil {
		t.Fatalf("create relationship: %v", err)
	}
	if rel.Direction != application.DirectionOutgoing || rel.Other.ID != child.ID || rel.Status != "ACTIVE" {
		t.Fatalf("relationship = %+v", rel)
	}

	// The same pair, type and an overlapping period is refused by the exclusion constraint.
	_, err = f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: child.ID, RelationshipType: "CHILD", ValidFrom: date(t, "2024-06-01"),
	})
	if !errors.Is(err, application.ErrRelationshipOverlap) {
		t.Fatalf("overlapping relationship: %v", err)
	}
	// A non-directional type is stored pair-ordered, so the mirrored duplicate also clashes.
	if _, err := f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: child.ID, RelationshipType: "SPOUSE", ValidFrom: date(t, "2020-01-01"),
	}); err != nil {
		t.Fatalf("spouse: %v", err)
	}
	if _, err := f.svc.CreateRelationship(ctx, rc, child.ID, application.NewRelationshipInput{
		TargetPersonID: parent.ID, RelationshipType: "SPOUSE", ValidFrom: date(t, "2021-01-01"),
	}); !errors.Is(err, application.ErrRelationshipOverlap) {
		t.Fatalf("mirrored spouse: %v", err)
	}

	// Seen from the child, the directional relationship comes back as INCOMING and the
	// non-directional one as MUTUAL.
	items, err := f.svc.ListRelationships(ctx, rc, child.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("relationships = %d, want 2", len(items))
	}
	byType := map[string]application.Relationship{}
	for _, it := range items {
		byType[it.RelationshipType] = it
	}
	if byType["CHILD"].Direction != application.DirectionIncoming || byType["CHILD"].Other.ID != parent.ID {
		t.Fatalf("child relationship = %+v", byType["CHILD"])
	}
	if byType["SPOUSE"].Direction != application.DirectionMutual {
		t.Fatalf("spouse relationship = %+v", byType["SPOUSE"])
	}

	// Self-relationships and unknown types are validation errors.
	if _, err := f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: parent.ID, RelationshipType: "CHILD", ValidFrom: date(t, "2020-01-01"),
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("self relationship: %v", err)
	}
	if _, err := f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: child.ID, RelationshipType: "BEST_FRIEND", ValidFrom: date(t, "2020-01-01"),
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown relationship type: %v", err)
	}
	// A person of another tenant is simply not found.
	stranger := f.create(t, f.tenantB, "Yabancı", "Kişi")
	if _, err := f.svc.CreateRelationship(ctx, rc, parent.ID, application.NewRelationshipInput{
		TargetPersonID: stranger.ID, RelationshipType: "CHILD", ValidFrom: date(t, "2020-01-01"),
	}); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("cross-tenant relationship: %v", err)
	}
}

func TestEndRelationshipClosesThePeriodUnderIfMatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	a := f.create(t, f.tenantA, "Bir", "Kişi")
	b := f.create(t, f.tenantA, "İki", "Kişi")

	rel, err := f.svc.CreateRelationship(ctx, rc, a.ID, application.NewRelationshipInput{
		TargetPersonID: b.ID, RelationshipType: "GUARDIAN", ValidFrom: date(t, "2020-01-01"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	end := application.EndRelationshipInput{EndsOn: date(t, "2026-01-01"), ReasonCode: "COURT_ORDER", ExpectedVersion: rel.RowVersion + 5}
	if _, err := f.svc.EndRelationship(ctx, rc, a.ID, rel.ID, end); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match: %v", err)
	}
	end.ExpectedVersion = rel.RowVersion
	ended, err := f.svc.EndRelationship(ctx, rc, a.ID, rel.ID, end)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != "ENDED" || ended.ValidTo == nil || !ended.ValidTo.Equal(date(t, "2026-01-01")) {
		t.Fatalf("ended = %+v", ended)
	}
	if ended.EndReasonCode == nil || *ended.EndReasonCode != "COURT_ORDER" || ended.RowVersion != rel.RowVersion+1 {
		t.Fatalf("ended = %+v", ended)
	}
	// An ended period no longer blocks a fresh one.
	if _, err := f.svc.CreateRelationship(ctx, rc, a.ID, application.NewRelationshipInput{
		TargetPersonID: b.ID, RelationshipType: "GUARDIAN", ValidFrom: date(t, "2026-02-01"),
	}); err != nil {
		t.Fatalf("relationship after end: %v", err)
	}
	// A relationship of a person the caller did not name is not found.
	other := f.create(t, f.tenantA, "Üç", "Kişi")
	if _, err := f.svc.EndRelationship(ctx, rc, other.ID, rel.ID, end); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("foreign relationship: %v", err)
	}
}

func TestMembershipsEnforcePrincipalOverlapAndMemberNumberScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	sponsorA := f.h.CreateTenantOrganization(f.tenantA, "Sponsor A", "SPONSOR")
	sponsorB := f.h.CreateTenantOrganization(f.tenantA, "Sponsor B", "SPONSOR")
	provider := f.h.CreateTenantOrganization(f.tenantA, "Sağlayıcı", "PROVIDER")

	employee := f.create(t, f.tenantA, "Çalışan", "Bir")
	principal, err := f.svc.CreateMembership(ctx, rc, employee.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorA, MembershipType: "EMPLOYEE",
		ExternalMemberNo: "EMP-1000", ValidFrom: date(t, "2024-01-01"),
	})
	if err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if principal.Status != "ACTIVE" || principal.SponsorDisplayName != "Sponsor A" || principal.RowVersion != 1 {
		t.Fatalf("membership = %+v", principal)
	}

	// An overlapping period under the same sponsor and type is refused by the database.
	if _, err := f.svc.CreateMembership(ctx, rc, employee.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorA, MembershipType: "EMPLOYEE", ValidFrom: date(t, "2025-01-01"),
	}); !errors.Is(err, application.ErrMembershipOverlap) {
		t.Fatalf("overlapping membership: %v", err)
	}
	// The external member number is unique per sponsor.
	colleague := f.create(t, f.tenantA, "Çalışan", "İki")
	if _, err := f.svc.CreateMembership(ctx, rc, colleague.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorA, MembershipType: "EMPLOYEE",
		ExternalMemberNo: "EMP-1000", ValidFrom: date(t, "2024-01-01"),
	}); !errors.Is(err, application.ErrMemberNoTaken) {
		t.Fatalf("duplicate member number: %v", err)
	}
	// ... but the same number under another sponsor is fine.
	if _, err := f.svc.CreateMembership(ctx, rc, colleague.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorB, MembershipType: "EMPLOYEE",
		ExternalMemberNo: "EMP-1000", ValidFrom: date(t, "2024-01-01"),
	}); err != nil {
		t.Fatalf("member number under another sponsor: %v", err)
	}

	// FAMILY requires a principal membership.
	dependant := f.create(t, f.tenantA, "Bağımlı", "Kişi")
	_, err = f.svc.CreateMembership(ctx, rc, dependant.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorA, MembershipType: "FAMILY", ValidFrom: date(t, "2024-01-01"),
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("dependant without principal: %v", err)
	}
	if ve.Fields[0].Field != "principalMembershipId" || ve.Fields[0].Code != domain.CodePrincipalRequired {
		t.Fatalf("fields = %+v", ve.Fields)
	}
	if _, err := f.svc.CreateMembership(ctx, rc, dependant.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsorA, MembershipType: "FAMILY",
		PrincipalMembershipID: &principal.ID, ValidFrom: date(t, "2024-01-01"),
	}); err != nil {
		t.Fatalf("dependant with principal: %v", err)
	}

	// A PROVIDER relationship is not a sponsor.
	if _, err := f.svc.CreateMembership(ctx, rc, dependant.ID, application.NewMembershipInput{
		SponsorOrganizationID: provider, MembershipType: "MEMBER", ValidFrom: date(t, "2024-01-01"),
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("provider as sponsor: %v", err)
	}
	if _, err := f.svc.CreateMembership(ctx, rc, dependant.ID, application.NewMembershipInput{
		SponsorOrganizationID: uuid.New(), MembershipType: "MEMBER", ValidFrom: date(t, "2024-01-01"),
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown sponsor: %v", err)
	}

	// The list shows both of the employee's memberships, newest first.
	items, err := f.svc.ListMemberships(ctx, rc, colleague.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("memberships = %d err=%v", len(items), err)
	}
}

func TestUpdateMembershipAppliesMergePatchUnderIfMatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	sponsor := f.h.CreateTenantOrganization(f.tenantA, "Sponsor", "PAYER")
	person := f.create(t, f.tenantA, "Üye", "Kişi")

	membership, err := f.svc.CreateMembership(ctx, rc, person.ID, application.NewMembershipInput{
		SponsorOrganizationID: sponsor, MembershipType: "MEMBER",
		ExternalMemberNo: "MEM-1", ValidFrom: date(t, "2024-01-01"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	suspended := "SUSPENDED"
	if _, err := f.svc.UpdateMembership(ctx, rc, person.ID, membership.ID, application.MembershipPatch{
		Status: &suspended, ExpectedVersion: membership.RowVersion + 3,
	}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match: %v", err)
	}
	validTo := date(t, "2026-12-31")
	updated, err := f.svc.UpdateMembership(ctx, rc, person.ID, membership.ID, application.MembershipPatch{
		Status: &suspended, ValidTo: &validTo, ExpectedVersion: membership.RowVersion,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != "SUSPENDED" || updated.ValidTo == nil || !updated.ValidTo.Equal(validTo) {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.ExternalMemberNo == nil || *updated.ExternalMemberNo != "MEM1" {
		t.Fatalf("member number lost on patch: %v", updated.ExternalMemberNo)
	}
	if updated.RowVersion != membership.RowVersion+1 {
		t.Fatalf("row version = %d", updated.RowVersion)
	}

	cleared, err := f.svc.UpdateMembership(ctx, rc, person.ID, membership.ID, application.MembershipPatch{
		ClearExternalMemberNo: true, ClearValidTo: true, ExpectedVersion: updated.RowVersion,
	})
	if err != nil || cleared.ExternalMemberNo != nil || cleared.ValidTo != nil {
		t.Fatalf("cleared = %+v err=%v", cleared, err)
	}
	bad := "PENDING"
	if _, err := f.svc.UpdateMembership(ctx, rc, person.ID, membership.ID, application.MembershipPatch{
		Status: &bad, ExpectedVersion: cleared.RowVersion,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("PENDING through patch: %v", err)
	}
	// The membership of another person is not found.
	other := f.create(t, f.tenantA, "Başka", "Kişi")
	if _, err := f.svc.UpdateMembership(ctx, rc, other.ID, membership.ID, application.MembershipPatch{
		Status: &suspended, ExpectedVersion: cleared.RowVersion,
	}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("foreign membership: %v", err)
	}
}

func TestSponsorScopedIdentifierNeedsASponsorAndIsUniquePerSponsor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	sponsorA := f.h.CreateTenantOrganization(f.tenantA, "Sponsor A", "SPONSOR")
	sponsorB := f.h.CreateTenantOrganization(f.tenantA, "Sponsor B", "SPONSOR")

	// Without a membership there is no scope to bind a SPONSOR-scoped identifier to.
	_, err := f.svc.Create(ctx, rc, domain.NewPerson{
		FirstName: "Sponsorsuz", LastName: "Kişi",
		Identifiers: []domain.SubmittedIdentifier{{Type: domain.TypeMemberNo, Value: "M-1000"}},
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Code != domain.CodeIdentifierScopeNeeded {
		t.Fatalf("sponsor-scoped identifier without a membership: %v", err)
	}

	addMemberNo := func(person application.Person, value string) (application.Person, error) {
		return f.svc.Update(ctx, rc, person.ID, domain.PersonPatch{
			Identifiers:     []domain.SubmittedIdentifier{{Type: domain.TypeMemberNo, Value: value, Primary: true}},
			ExpectedVersion: person.RowVersion,
		})
	}
	enrol := func(person application.Person, sponsor uuid.UUID) {
		t.Helper()
		if _, err := f.svc.CreateMembership(ctx, rc, person.ID, application.NewMembershipInput{
			SponsorOrganizationID: sponsor, MembershipType: "EMPLOYEE", ValidFrom: date(t, "2024-01-01"),
		}); err != nil {
			t.Fatalf("enrol: %v", err)
		}
	}

	first := f.create(t, f.tenantA, "Üye", "Bir")
	enrol(first, sponsorA)
	stored, err := addMemberNo(first, "M-1000")
	if err != nil {
		t.Fatalf("add member number: %v", err)
	}
	if len(stored.Identifiers) != 1 || stored.Identifiers[0].MaskedValue != "**000" {
		t.Fatalf("identifiers = %+v", stored.Identifiers)
	}

	// The same number under a different sponsor is a different scope key.
	second := f.create(t, f.tenantA, "Üye", "İki")
	enrol(second, sponsorB)
	if _, err := addMemberNo(second, "M-1000"); err != nil {
		t.Fatalf("same number under another sponsor: %v", err)
	}
	// Under the same sponsor it collides.
	third := f.create(t, f.tenantA, "Üye", "Üç")
	enrol(third, sponsorA)
	if _, err := addMemberNo(third, "M-1000"); !errors.Is(err, application.ErrIdentifierTaken) {
		t.Fatalf("same number under the same sponsor: %v", err)
	}

	// Searching a SPONSOR-scoped type needs the sponsor, and finds only that sponsor's row.
	if _, err := f.svc.SearchByIdentifier(ctx, rc, application.SearchInput{Type: domain.TypeMemberNo, Value: "M-1000"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("search without a sponsor: %v", err)
	}
	found, err := f.svc.SearchByIdentifier(ctx, rc, application.SearchInput{
		Type: domain.TypeMemberNo, Value: "M-1000", SponsorID: sponsorB,
	})
	if err != nil || found.ID != second.ID {
		t.Fatalf("scoped search = %+v err=%v", found, err)
	}
}
