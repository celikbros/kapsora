// Package application implements the person use cases: registry create/get/patch/list,
// blind-index search, family relationships and sponsor memberships. Transactions are
// opened here with db.WithTenantTx so a change and its audit row commit together.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Errors mapped by the transport layer to problem+json codes.
var (
	ErrPersonNotFound       = errors.New("party: person not found")
	ErrNotFound             = errors.New("party: resource not found")
	ErrIdentifierTaken      = errors.New("party: identifier already belongs to a person")
	ErrRelationshipOverlap  = errors.New("party: relationship period overlaps an existing one")
	ErrMembershipOverlap    = errors.New("party: membership period overlaps an existing one")
	ErrMemberNoTaken        = errors.New("party: external member number already used by this sponsor")
	ErrVersionMismatch      = errors.New("party: row version does not match If-Match")
	ErrCatalogEntryNotFound = errors.New("party: catalog entry not found")
)

// IdentifierTakenError reports which person already carries the identifier. The transport
// layer reveals ExistingPersonID only to callers holding member.identifier.search.
type IdentifierTakenError struct {
	ExistingPersonID uuid.UUID
}

func (e *IdentifierTakenError) Error() string { return ErrIdentifierTaken.Error() }

// Is makes errors.Is(err, ErrIdentifierTaken) match.
func (e *IdentifierTakenError) Is(target error) bool { return target == ErrIdentifierTaken }

// CatalogType is one row of any of the three party catalogs; only the flag relevant to
// the catalog is filled.
type CatalogType struct {
	Code              string
	DisplayName       string
	Status            string
	IsSensitive       bool
	UniquenessScope   string
	IsDirectional     bool
	RequiresPrincipal bool
}

// NewPersonRow is the insert payload of party.person.
type NewPersonRow struct {
	TenantID       uuid.UUID
	ActorID        uuid.UUID
	FirstName      string
	MiddleName     *string
	LastName       string
	NormalizedName string
	BirthDate      *time.Time
	SexAtBirth     *string
}

// PersonUpdateRow is the full new state of party.person after a merge-patch.
type PersonUpdateRow struct {
	ActorID        uuid.UUID
	FirstName      string
	MiddleName     *string
	LastName       string
	NormalizedName string
	BirthDate      *time.Time
	SexAtBirth     *string
	Status         string
}

// PersonRow is party.person as stored.
type PersonRow struct {
	ID           uuid.UUID
	FirstName    string
	MiddleName   *string
	LastName     string
	BirthDate    *time.Time
	SexAtBirth   *string
	Status       string
	MergedIntoID *uuid.UUID
	CreatedAt    time.Time
	RowVersion   int64
}

// StoredIdentifier is a party.person_identifier row; the cipher stays inside the service.
type StoredIdentifier struct {
	ID          uuid.UUID
	Type        string
	Cipher      []byte
	MaskedValue string
	ScopeKey    string
	Primary     bool
}

// NewIdentifier is the insert payload of party.person_identifier.
type NewIdentifier struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	PersonID    uuid.UUID
	Type        string
	Cipher      []byte
	Hash        []byte
	MaskedValue string
	ScopeKey    string
	Primary     bool
}

// Summary is one list row.
type Summary struct {
	ID                      uuid.UUID
	FirstName               string
	MiddleName              *string
	LastName                string
	Status                  string
	MaskedPrimaryIdentifier string
	CreatedAt               time.Time
}

// ListQuery is the repository-level person filter.
type ListQuery struct {
	Status    string
	Pattern   string
	SponsorID uuid.UUID
	After     *httpx.Cursor
	PageSize  int
}

// RelationshipRow is party.person_relationship joined with its type and, for list rows,
// the other person.
type RelationshipRow struct {
	ID               uuid.UUID
	SourcePersonID   uuid.UUID
	TargetPersonID   uuid.UUID
	RelationshipType string
	IsDirectional    bool
	Status           string
	ValidFrom        time.Time
	ValidTo          *time.Time
	EndReasonCode    *string
	RowVersion       int64
	Other            Summary
}

// NewRelationshipRow is the insert payload of party.person_relationship.
type NewRelationshipRow struct {
	TenantID         uuid.UUID
	SourcePersonID   uuid.UUID
	TargetPersonID   uuid.UUID
	RelationshipType string
	ValidFrom        time.Time
	ValidTo          *time.Time
}

// EndRelationshipRow closes a relationship period.
type EndRelationshipRow struct {
	EndsOn     time.Time
	ReasonCode string
	ReasonText *string
	Expected   int64
}

// SponsorOrganization is the tenant's relationship with a sponsor or payer.
type SponsorOrganization struct {
	ID          uuid.UUID
	Role        string
	Status      string
	DisplayName string
}

// MembershipRow is party.sponsor_membership as stored, with the sponsor's display name.
type MembershipRow struct {
	ID                    uuid.UUID
	PersonID              uuid.UUID
	SponsorOrganizationID uuid.UUID
	SponsorDisplayName    string
	PrincipalMembershipID *uuid.UUID
	MembershipType        string
	ExternalMemberNo      *string
	Status                string
	ValidFrom             time.Time
	ValidTo               *time.Time
	SourceSystem          *string
	RowVersion            int64
}

// NewMembershipRow is the insert payload of party.sponsor_membership.
type NewMembershipRow struct {
	TenantID              uuid.UUID
	PersonID              uuid.UUID
	SponsorOrganizationID uuid.UUID
	PrincipalMembershipID *uuid.UUID
	MembershipType        string
	ExternalMemberNo      *string
	Status                string
	ValidFrom             time.Time
	ValidTo               *time.Time
}

// MembershipUpdateRow is the full new state of a membership after a merge-patch.
type MembershipUpdateRow struct {
	Status           string
	ExternalMemberNo *string
	ValidTo          *time.Time
	Expected         int64
}

// Repository is the persistence port; every method runs inside the caller's transaction.
type Repository interface {
	ListIdentifierTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]CatalogType, error)
	GetIdentifierType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (CatalogType, error)
	ListRelationshipTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]CatalogType, error)
	GetRelationshipType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (CatalogType, error)
	ListMembershipTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]CatalogType, error)
	GetMembershipType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (CatalogType, error)

	CreatePerson(ctx context.Context, tx pgx.Tx, in NewPersonRow) (uuid.UUID, error)
	GetPerson(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (PersonRow, error)
	UpdatePerson(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, in PersonUpdateRow, expected int64) error
	ListPeople(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ListQuery) ([]Summary, error)

	// AddIdentifier inserts one identifier row; a blind-index collision inside the type's
	// uniqueness scope returns *IdentifierTakenError with the owning person.
	AddIdentifier(ctx context.Context, tx pgx.Tx, in NewIdentifier) error
	ListIdentifiers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]StoredIdentifier, error)
	RemoveIdentifiers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, typeCode string) (int64, error)
	FindPersonByIdentifierHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, typeCode string, scopeKey *string, hash []byte) (uuid.UUID, bool, error)
	ActiveSponsorOrganizations(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]uuid.UUID, error)

	ListRelationships(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]RelationshipRow, error)
	GetRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID) (RelationshipRow, error)
	CreateRelationship(ctx context.Context, tx pgx.Tx, in NewRelationshipRow) (uuid.UUID, error)
	EndRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID, in EndRelationshipRow) error

	GetSponsorOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (SponsorOrganization, error)
	ListMemberships(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]MembershipRow, error)
	GetMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID) (MembershipRow, error)
	CreateMembership(ctx context.Context, tx pgx.Tx, in NewMembershipRow) (uuid.UUID, error)
	UpdateMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID, in MembershipUpdateRow) error
}
