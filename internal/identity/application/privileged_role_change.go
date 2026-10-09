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
	ErrRoleChangePendingExists        = errors.New("identity: role change pending exists")
	ErrRoleChangeNotPending           = errors.New("identity: role change not pending")
	ErrRoleChangeTargetChanged        = errors.New("identity: role change target changed")
	ErrRoleChangeConfigurationChanged = errors.New("identity: role change configuration changed")
	ErrRoleChangeMakerUnauthorized    = errors.New("identity: role change maker unauthorized")
	ErrRoleChangeSameActor            = errors.New("identity: maker and checker are same actor")
	ErrRoleChangeCancelForbidden      = errors.New("identity: role change cancel forbidden")
	ErrRoleChangeNoChecker            = errors.New("identity: no eligible checker")
	ErrRoleChangeKeyReused            = errors.New("identity: role change idempotency key reused")
)

var privilegedRoleCodes = []string{"CONTRACT_PUBLISHER", "PAYER_APPROVER", "PLAN_PUBLISHER", "RULE_APPROVER", "TENANT_ADMIN"}

func ProtectedPrivilegedRole(code string) bool {
	for _, candidate := range privilegedRoleCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

func PrivilegedRoleCodes() []string { return append([]string(nil), privilegedRoleCodes...) }

type RoleChangePermission struct {
	Code        string `json:"code"`
	Sensitivity string `json:"sensitivity"`
}
type RoleChangeValidity struct {
	From          *string `json:"from"`
	FromInclusive bool    `json:"fromInclusive"`
	To            *string `json:"to"`
	ToInclusive   bool    `json:"toInclusive"`
}
type PrivilegedRoleOption struct {
	Code                    string   `json:"code"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	ScopeType               string   `json:"scopeType"`
	PermissionCodes         []string `json:"permissionCodes"`
	HasSensitivePermissions bool     `json:"hasSensitivePermissions"`
	ConfigurationHash       string   `json:"configurationHash"`
	RequiresApproval        bool     `json:"requiresApproval"`
}
type RoleChangeEligibility struct {
	MembershipID          uuid.UUID   `json:"membershipId"`
	MembershipRowVersion  int64       `json:"membershipRowVersion"`
	CanRequestAssignment  bool        `json:"canRequestAssignment"`
	AssignmentRefusalCode *string     `json:"assignmentRefusalCode"`
	RevokeGrantIDs        []uuid.UUID `json:"revokeGrantIds"`
	CheckerAvailability   string      `json:"checkerAvailability"`
}
type RoleChangeRequest struct {
	ID                       uuid.UUID              `json:"id"`
	Operation                string                 `json:"operation"`
	Status                   string                 `json:"status"`
	TargetMembershipID       uuid.UUID              `json:"targetMembershipId"`
	MakerMembershipID        uuid.UUID              `json:"makerMembershipId"`
	RoleCode                 string                 `json:"roleCode"`
	ScopeType                string                 `json:"scopeType"`
	PermissionSnapshot       []RoleChangePermission `json:"permissionSnapshot"`
	ConfigurationHash        string                 `json:"configurationHash"`
	TargetMembershipVersion  int64                  `json:"targetMembershipVersion"`
	RevokeGrantID            *uuid.UUID             `json:"revokeGrantId"`
	RevokeValidity           *RoleChangeValidity    `json:"revokeValidity"`
	ReasonCode               string                 `json:"reasonCode"`
	CreatedAt                time.Time              `json:"createdAt"`
	RowVersion               int64                  `json:"rowVersion"`
	DecidedAt                *time.Time             `json:"decidedAt"`
	DecidedByMembershipID    *uuid.UUID             `json:"decidedByMembershipId"`
	DecisionReasonCode       *string                `json:"decisionReasonCode"`
	AppliedGrantID           *uuid.UUID             `json:"appliedGrantId"`
	AppliedValidity          *RoleChangeValidity    `json:"appliedValidity"`
	AppliedMembershipVersion *int64                 `json:"appliedMembershipVersion"`
}
type RoleChangeAppliedGrant struct {
	ID            uuid.UUID `json:"id"`
	RoleCode      string    `json:"roleCode"`
	RoleName      string    `json:"roleName"`
	IsSystemRole  bool      `json:"isSystemRole"`
	ScopeType     string    `json:"scopeType"`
	ValidFrom     *string   `json:"validFrom"`
	ValidTo       *string   `json:"validTo"`
	ValidityEmpty bool      `json:"validityEmpty"`
}
type RoleChangeCommandResult struct {
	Request              RoleChangeRequest       `json:"request"`
	AppliedGrant         *RoleChangeAppliedGrant `json:"appliedGrant"`
	MembershipRowVersion *int64                  `json:"membershipRowVersion"`
}
type RoleChangeResponse struct {
	Status   int
	ETag     string
	Body     []byte
	Replayed bool
}
type RoleChangeCommand struct {
	Code               string
	TargetMembershipID uuid.UUID
	RequestID          uuid.UUID
	Version            int64
	Operation          string
	RoleCode           string
	GrantID            uuid.UUID
	ConfigurationHash  string
	ReasonCode         string
	KeyHash            []byte
	RequestHash        []byte
}
type RoleChangeRequestDetail struct {
	Request                 RoleChangeRequest `json:"request"`
	CanApprove              bool              `json:"canApprove"`
	ApprovalRefusalCode     *string           `json:"approvalRefusalCode"`
	CanReject               bool              `json:"canReject"`
	RejectionRefusalCode    *string           `json:"rejectionRefusalCode"`
	CanCancel               bool              `json:"canCancel"`
	CancellationRefusalCode *string           `json:"cancellationRefusalCode"`
	CheckerAvailability     string            `json:"checkerAvailability"`
}
type RoleChangeRequestSummary struct {
	ID                 uuid.UUID  `json:"id"`
	Operation          string     `json:"operation"`
	Status             string     `json:"status"`
	TargetMembershipID uuid.UUID  `json:"targetMembershipId"`
	MakerMembershipID  uuid.UUID  `json:"makerMembershipId"`
	RoleCode           string     `json:"roleCode"`
	ScopeType          string     `json:"scopeType"`
	ReasonCode         string     `json:"reasonCode"`
	CreatedAt          time.Time  `json:"createdAt"`
	RowVersion         int64      `json:"rowVersion"`
	DecidedAt          *time.Time `json:"decidedAt"`
}
type RoleChangeRequestPage struct {
	Items      []RoleChangeRequestSummary `json:"items"`
	NextCursor *string                    `json:"nextCursor"`
}

type PrivilegedRoleChangeRepository interface {
	Authorize(context.Context, identity.RequestContext, bool) error
	Options(context.Context, identity.RequestContext) ([]PrivilegedRoleOption, error)
	Eligibility(context.Context, identity.RequestContext, uuid.UUID) (RoleChangeEligibility, error)
	List(context.Context, identity.RequestContext, string, *uuid.UUID, *httpx.Cursor, int) ([]RoleChangeRequestSummary, error)
	Detail(context.Context, identity.RequestContext, uuid.UUID) (RoleChangeRequestDetail, error)
	Preflight(context.Context, identity.RequestContext, RoleChangeCommand) (*RoleChangeResponse, error)
	Execute(context.Context, identity.RequestContext, RoleChangeCommand) (RoleChangeResponse, error)
}

type PrivilegedRoleChangeService struct {
	Repo    PrivilegedRoleChangeRepository
	Cursors *httpx.CursorCodec
}

func NewPrivilegedRoleChangeService(repo PrivilegedRoleChangeRepository, cursors *httpx.CursorCodec) *PrivilegedRoleChangeService {
	return &PrivilegedRoleChangeService{Repo: repo, Cursors: cursors}
}
