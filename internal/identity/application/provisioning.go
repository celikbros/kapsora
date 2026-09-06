package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
)

// ProvisionCommand creates a tenant. Locale, time zone and currency default to Turkish
// settings when empty.
type ProvisionCommand struct {
	Code        string
	LegalName   string
	DisplayName string
	Locale      string
	TimeZone    string
	Currency    string
	// RequestedBy is the acting actor for audit; uuid.Nil for a seed run.
	RequestedBy uuid.UUID
}

var tenantCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,39}$`)

// ErrTenantCodeExists is returned when the tenant code is already taken.
var ErrTenantCodeExists = errors.New("identity: tenant code already exists")

// CatalogEntry is a baseline reference row seeded into every tenant.
type CatalogEntry struct {
	Code        string
	DisplayName string
	// Flag carries the type-specific boolean: is_sensitive, is_directional, requires_principal.
	Flag bool
	// Scope is identifier_type.uniqueness_scope; empty for other catalogs.
	Scope string
}

// BaselineCatalogs are the tenant-configurable type catalogs every tenant starts with
// (v1.2 section 16.3). Customers add their own rows as data, never as migrations.
type BaselineCatalogs struct {
	IdentifierTypes   []CatalogEntry
	RelationshipTypes []CatalogEntry
	MembershipTypes   []CatalogEntry
	ProgramTypes      []CatalogEntry
}

// DefaultBaselineCatalogs returns the documented baseline.
func DefaultBaselineCatalogs() BaselineCatalogs {
	return BaselineCatalogs{
		IdentifierTypes: []CatalogEntry{
			{Code: "TCKN", DisplayName: "T.C. Kimlik No", Flag: true, Scope: "TENANT"},
			{Code: "PASSPORT", DisplayName: "Pasaport No", Flag: true, Scope: "TENANT"},
			{Code: "MEMBER_NO", DisplayName: "Üye No", Flag: false, Scope: "SPONSOR"},
			{Code: "CUSTOMER_NO", DisplayName: "Müşteri No", Flag: false, Scope: "SPONSOR"},
		},
		RelationshipTypes: []CatalogEntry{
			{Code: "SPOUSE", DisplayName: "Eş", Flag: false},
			{Code: "CHILD", DisplayName: "Çocuk", Flag: true},
			{Code: "PARENT", DisplayName: "Ebeveyn", Flag: true},
			{Code: "DEPENDENT", DisplayName: "Bağımlı", Flag: true},
			{Code: "GUARDIAN", DisplayName: "Vasi", Flag: true},
			{Code: "DELEGATE", DisplayName: "Delege", Flag: true},
		},
		MembershipTypes: []CatalogEntry{
			{Code: "EMPLOYEE", DisplayName: "Çalışan"},
			{Code: "RETIREE", DisplayName: "Emekli"},
			{Code: "MEMBER", DisplayName: "Üye"},
			{Code: "CUSTOMER", DisplayName: "Müşteri"},
			{Code: "INSURED", DisplayName: "Sigortalı"},
			{Code: "STUDENT", DisplayName: "Öğrenci"},
			{Code: "BENEFICIARY", DisplayName: "Yararlanıcı"},
			{Code: "FAMILY", DisplayName: "Aile Bireyi", Flag: true},
		},
		ProgramTypes: []CatalogEntry{
			{Code: "EMPLOYEE_BENEFIT", DisplayName: "Çalışan Faydası"},
			{Code: "MEMBER_PROGRAM", DisplayName: "Üye Programı"},
			{Code: "SOCIAL_SUPPORT", DisplayName: "Sosyal Destek"},
			{Code: "CUSTOMER_PRIVILEGE", DisplayName: "Müşteri Ayrıcalığı"},
			{Code: "INSURANCE_ASSISTANCE", DisplayName: "Sigorta Asistansı"},
			{Code: "STUDENT_SUPPORT", DisplayName: "Öğrenci Desteği"},
		},
	}
}

// GrantRoleInput binds a system role to an actor in a tenant, creating the membership when
// missing. Idempotent: an identical open-ended grant is not duplicated.
type GrantRoleInput struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID
	RoleCode string
	// ScopeType is ScopeTenant, ScopeOrganization or ScopePerson. A PERSON grant is what
	// binds a member account to the one person it acts for; the scope id is the person and
	// the database refuses one that does not exist in this tenant, is already bound to
	// another account, or would be the membership's second person.
	ScopeType string
	ScopeID   uuid.NullUUID // required for non-TENANT scopes
	GrantedBy uuid.NullUUID
	Reason    string
}

// ProvisioningRepository performs the tenant-creating and grant-writing transactions.
type ProvisioningRepository interface {
	ProvisionTenant(ctx context.Context, cmd ProvisionCommand, roles []RoleTemplate, catalogs BaselineCatalogs) (uuid.UUID, error)
	GrantRole(ctx context.Context, in GrantRoleInput) (created bool, err error)
	// EnsureProviderOrganization returns the tenant organization with tenantCode, creating
	// a PROVIDER relationship (and a bare global organization) when missing. Demo/seed only:
	// the real organization flow with tax-number deduplication is WP-I1-03's.
	EnsureProviderOrganization(ctx context.Context, tenantID uuid.UUID, tenantCode, displayName string) (uuid.UUID, error)
}

// Provisioner creates tenants and issues role grants.
type Provisioner struct {
	repo  ProvisioningRepository
	audit AuditSink
}

// NewProvisioner wires the provisioner; audit may be nil.
func NewProvisioner(repo ProvisioningRepository, sink AuditSink) *Provisioner {
	if sink == nil {
		sink = func(context.Context, audit.Event) error { return nil }
	}
	return &Provisioner{repo: repo, audit: sink}
}

// ProvisionTenant creates the tenant, its baseline catalogs and its system roles in one
// transaction (v1.2 section 9.1), then audits tenant.provision.
func (p *Provisioner) ProvisionTenant(ctx context.Context, cmd ProvisionCommand) (uuid.UUID, error) {
	if !tenantCodePattern.MatchString(cmd.Code) {
		return uuid.Nil, fmt.Errorf("identity: tenant code %q must match %s", cmd.Code, tenantCodePattern)
	}
	if cmd.LegalName == "" || cmd.DisplayName == "" {
		return uuid.Nil, errors.New("identity: tenant legal name and display name are required")
	}
	if cmd.Locale == "" {
		cmd.Locale = "tr-TR"
	}
	if cmd.TimeZone == "" {
		cmd.TimeZone = "Europe/Istanbul"
	}
	if cmd.Currency == "" {
		cmd.Currency = "TRY"
	}
	id, err := p.repo.ProvisionTenant(ctx, cmd, RoleTemplates(), DefaultBaselineCatalogs())
	if err != nil {
		return uuid.Nil, err
	}
	_ = p.audit(ctx, audit.Event{
		TenantID:   nullUUID(id),
		ActorID:    nullUUID(cmd.RequestedBy),
		Category:   audit.CategoryAdmin,
		ActionCode: "tenant.provision",
		Outcome:    audit.OutcomeSuccess,
		Detail:     map[string]any{"tenant_code": cmd.Code},
	})
	return id, nil
}

// GrantRole issues a role grant; unknown role codes and scope/role mismatches are refused.
func (p *Provisioner) GrantRole(ctx context.Context, in GrantRoleInput) (bool, error) {
	tpl, ok := RoleTemplateByCode(in.RoleCode)
	if !ok {
		return false, fmt.Errorf("identity: unknown role template %q", in.RoleCode)
	}
	if in.ScopeType == "" {
		in.ScopeType = tpl.Scope
	}
	if in.ScopeType == ScopeTenant && in.ScopeID.Valid {
		return false, errors.New("identity: a tenant-scoped grant carries no scope id")
	}
	if in.ScopeType != ScopeTenant && !in.ScopeID.Valid {
		return false, fmt.Errorf("identity: a %s-scoped grant needs a scope id", in.ScopeType)
	}
	created, err := p.repo.GrantRole(ctx, in)
	if err != nil {
		return false, err
	}
	if created {
		_ = p.audit(ctx, audit.Event{
			TenantID:   nullUUID(in.TenantID),
			ActorID:    in.GrantedBy,
			Category:   audit.CategoryAdmin,
			ActionCode: "access_grant.create",
			Outcome:    audit.OutcomeSuccess,
			Detail:     map[string]any{"role_code": in.RoleCode, "scope_type": in.ScopeType, "target_actor_id": in.ActorID},
		})
	}
	return created, nil
}

// EnsureProviderOrganization is the seed helper described on the repository port.
func (p *Provisioner) EnsureProviderOrganization(ctx context.Context, tenantID uuid.UUID, tenantCode, displayName string) (uuid.UUID, error) {
	return p.repo.EnsureProviderOrganization(ctx, tenantID, tenantCode, displayName)
}
