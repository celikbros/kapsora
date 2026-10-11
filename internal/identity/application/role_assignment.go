package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

var (
	ErrRoleAssignmentInvalid        = errors.New("identity: invalid role assignment")
	ErrRoleAssignmentNotFound       = errors.New("identity: role assignment resource not found")
	ErrRoleAssignmentSelf           = errors.New("identity: self role change forbidden")
	ErrRoleConfigurationUnsupported = errors.New("identity: role configuration unsupported")
	ErrRoleAssignmentUnsupported    = errors.New("identity: role assignment unsupported")
	ErrRoleMembershipConflict       = errors.New("identity: role assignment membership state conflict")
	ErrExistingAccessConflict       = errors.New("identity: existing access conflict")
	ErrRoleGrantConflict            = errors.New("identity: grant state conflict")
	ErrRoleAssignmentVersion        = errors.New("identity: role assignment version mismatch")
	ErrLastTenantRoleManager        = errors.New("identity: last tenant role manager")
)

var supportedRoleScopes = map[string]string{
	"PROGRAM_MANAGER": ScopeTenant, "CONTRACT_MANAGER": ScopeTenant,
	"RULE_AUTHOR": ScopeTenant, "MEDICAL_REVIEWER": ScopeTenant,
	"FINANCIAL_REVIEWER": ScopeTenant, "AUDITOR": ScopeTenant,
	"SPONSOR_HR":     ScopeTenant,
	"PROVIDER_ADMIN": ScopeOrganization, "PROVIDER_STAFF": ScopeOrganization,
	"PROVIDER_BILLING": ScopeOrganization, "PROVIDER_RESERVATION": ScopeOrganization,
}

func SupportedRoleScope(code string) (string, bool) {
	scope, ok := supportedRoleScopes[code]
	return scope, ok
}

type RoleAssignmentOption struct {
	Code                    string
	Name                    string
	Description             string
	ScopeType               string
	PermissionCodes         []string
	HasSensitivePermissions bool
}

type RoleAssignmentOrganization struct {
	ID          uuid.UUID
	DisplayName string
	TenantCode  *string
	CreatedAt   time.Time
}

type TenantRoleGrant struct {
	ID                         uuid.UUID
	RoleCode                   string
	RoleName                   string
	IsSystemRole               bool
	ScopeType                  string
	OrganizationRelationshipID *uuid.UUID
	OrganizationDisplayName    *string
	ValidFrom                  *string
	ValidTo                    *string
	ValidityEmpty              bool
	CanRevoke                  bool
	RevocationRefusalCode      *string
	CreatedAt                  time.Time
}

type RoleGrantList struct {
	MembershipID          uuid.UUID
	MembershipRowVersion  int64
	CanAssign             bool
	AssignmentRefusalCode *string
	Items                 []TenantRoleGrant
	NextCursor            string
}

type RoleOrganizationPage struct {
	Items      []RoleAssignmentOrganization
	NextCursor string
}

type RoleGrantResult struct {
	MembershipID         uuid.UUID
	MembershipRowVersion int64
	Grant                TenantRoleGrant
}

type AssignRoleGrantInput struct {
	RoleCode                   string
	ScopeType                  string
	OrganizationRelationshipID uuid.NullUUID
	ReasonCode                 string
}

type RoleAssignmentRepository interface {
	Authorize(ctx context.Context, rc identity.RequestContext) error
	AuthorizeCommand(ctx context.Context, rc identity.RequestContext) error
	Options(ctx context.Context, rc identity.RequestContext) ([]RoleAssignmentOption, error)
	Organizations(ctx context.Context, rc identity.RequestContext, after *httpx.Cursor, limit int) ([]RoleAssignmentOrganization, error)
	Grants(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, after *httpx.Cursor, limit int) (RoleGrantList, error)
	Assign(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, expectedVersion int64, input AssignRoleGrantInput) (RoleGrantResult, error)
	Revoke(ctx context.Context, rc identity.RequestContext, membershipID, grantID uuid.UUID, expectedVersion int64, reasonCode string) (RoleGrantResult, error)
}

type RoleAssignmentService struct {
	repo    RoleAssignmentRepository
	cursors *httpx.CursorCodec
}

func NewRoleAssignmentService(repo RoleAssignmentRepository, cursors *httpx.CursorCodec) *RoleAssignmentService {
	return &RoleAssignmentService{repo: repo, cursors: cursors}
}

func (s *RoleAssignmentService) Authorize(ctx context.Context, rc identity.RequestContext) error {
	return s.repo.Authorize(ctx, rc)
}

func (s *RoleAssignmentService) AuthorizeCommand(ctx context.Context, rc identity.RequestContext) error {
	return s.repo.AuthorizeCommand(ctx, rc)
}

func (s *RoleAssignmentService) Options(ctx context.Context, rc identity.RequestContext) ([]RoleAssignmentOption, error) {
	return s.repo.Options(ctx, rc)
}

func (s *RoleAssignmentService) Organizations(ctx context.Context, rc identity.RequestContext, cursor string, limit int) (RoleOrganizationPage, error) {
	after, err := s.decode(cursor)
	if err != nil {
		return RoleOrganizationPage{}, err
	}
	pageSize := httpx.ClampLimit(limit)
	items, err := s.repo.Organizations(ctx, rc, after, pageSize+1)
	if err != nil {
		return RoleOrganizationPage{}, err
	}
	page := RoleOrganizationPage{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

func (s *RoleAssignmentService) Grants(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, cursor string, limit int) (RoleGrantList, error) {
	after, err := s.decode(cursor)
	if err != nil {
		return RoleGrantList{}, err
	}
	pageSize := httpx.ClampLimit(limit)
	page, err := s.repo.Grants(ctx, rc, membershipID, after, pageSize+1)
	if err != nil {
		return RoleGrantList{}, err
	}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

func (s *RoleAssignmentService) decode(raw string) (*httpx.Cursor, error) {
	if s.cursors == nil {
		return nil, errors.New("identity: role assignment cursor unavailable")
	}
	cur, present, err := s.cursors.Decode(raw)
	if err != nil {
		return nil, err
	}
	if present {
		return &cur, nil
	}
	return nil, nil
}

func (s *RoleAssignmentService) Assign(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, version int64, in AssignRoleGrantInput) (RoleGrantResult, error) {
	if version < 1 {
		return RoleGrantResult{}, ErrRoleAssignmentVersion
	}
	if in.ScopeType != ScopeTenant && in.ScopeType != ScopeOrganization {
		return RoleGrantResult{}, ErrRoleAssignmentInvalid
	}
	scope, ok := SupportedRoleScope(in.RoleCode)
	if !ok || scope != in.ScopeType {
		return RoleGrantResult{}, ErrRoleAssignmentUnsupported
	}
	if (scope == ScopeTenant && in.OrganizationRelationshipID.Valid) || (scope == ScopeOrganization && !in.OrganizationRelationshipID.Valid) {
		return RoleGrantResult{}, ErrRoleAssignmentInvalid
	}
	if in.ReasonCode != "ONBOARDING" && in.ReasonCode != "DUTY_ASSIGNMENT" {
		return RoleGrantResult{}, ErrRoleAssignmentInvalid
	}
	return s.repo.Assign(ctx, rc, membershipID, version, in)
}

func (s *RoleAssignmentService) Revoke(ctx context.Context, rc identity.RequestContext, membershipID, grantID uuid.UUID, version int64, reasonCode string) (RoleGrantResult, error) {
	if version < 1 {
		return RoleGrantResult{}, ErrRoleAssignmentVersion
	}
	if reasonCode != "ACCESS_REVIEW" && reasonCode != "DUTY_ENDED" && reasonCode != "SECURITY_CONCERN" {
		return RoleGrantResult{}, ErrRoleAssignmentInvalid
	}
	return s.repo.Revoke(ctx, rc, membershipID, grantID, version, reasonCode)
}
