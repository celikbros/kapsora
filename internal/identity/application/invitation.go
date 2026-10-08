package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
)

const InvitationDeliveryEvent = "identity.invitation.delivery_requested"

var (
	ErrInvitationUnavailable        = errors.New("identity: invitation unavailable")
	ErrInvitationNotFound           = errors.New("identity: invitation not found")
	ErrInvitationPendingExists      = errors.New("identity: pending invitation exists")
	ErrInvitationStateConflict      = errors.New("identity: invitation state conflict")
	ErrInvitationMembershipConflict = errors.New("identity: existing membership cannot be reactivated")
	ErrInvitationVersionConflict    = errors.New("identity: invitation version changed")
	ErrInvitationKeyReused          = errors.New("identity: invitation command key reused")
	ErrInvitationInvalidEmail       = errors.New("identity: invalid invitation email")
	ErrInvitationDeliveryDisabled   = errors.New("identity: invitation delivery disabled")
)

var invitationDomain = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// NormalizeInvitationEmail keeps the local part byte-for-byte and normalizes only
// the domain. Provider-specific plus and dot rules are deliberately excluded.
func NormalizeInvitationEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if len(email) < 3 || len(email) > 254 || strings.ContainsAny(email, "\r\n\t <>\x00") || strings.Count(email, "@") != 1 {
		return "", ErrInvitationInvalidEmail
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", ErrInvitationInvalidEmail
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts[0]) > 64 || len(parts[0]) == 0 || !invitationDomain.MatchString(parts[1]) || !strings.Contains(parts[1], ".") || strings.Contains(parts[1], "..") {
		return "", ErrInvitationInvalidEmail
	}
	return parts[0] + "@" + strings.ToLower(parts[1]), nil
}

func MaskInvitationEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return "***"
	}
	// Do not retain a full local part or domain in a manager-visible summary.
	return string([]rune(parts[0])[0]) + "***@***"
}

// GenerateInvitationCode returns a bearer proof with 32 random secret bytes.
// The UUID selectors are routing hints and convey no authority before Verify.
func GenerateInvitationCode(tenantID, invitationID uuid.UUID) (code string, digest []byte, err error) {
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", nil, err
	}
	code = "v1." + tenantID.String() + "." + invitationID.String() + "." + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(code))
	return code, sum[:], nil
}

type InvitationProof struct {
	TenantID     uuid.UUID
	InvitationID uuid.UUID
	Code         string
}

func ParseInvitationCode(code string) (InvitationProof, error) {
	if len(code) != 120 { // v1 + two UUIDs + 43 encoded secret bytes + three separators
		return InvitationProof{}, ErrInvitationUnavailable
	}
	parts := strings.Split(code, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return InvitationProof{}, ErrInvitationUnavailable
	}
	tenantID, err := uuid.Parse(parts[1])
	if err != nil || tenantID == uuid.Nil {
		return InvitationProof{}, ErrInvitationUnavailable
	}
	invitationID, err := uuid.Parse(parts[2])
	if err != nil || invitationID == uuid.Nil {
		return InvitationProof{}, ErrInvitationUnavailable
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != parts[3] {
		return InvitationProof{}, ErrInvitationUnavailable
	}
	return InvitationProof{TenantID: tenantID, InvitationID: invitationID, Code: code}, nil
}

func VerifyInvitationProof(code string, digest []byte) bool {
	sum := sha256.Sum256([]byte(code))
	return len(digest) == sha256.Size && subtle.ConstantTimeCompare(sum[:], digest) == 1
}

type TenantInvitation struct {
	ID              uuid.UUID `json:"invitationId"`
	MaskedRecipient string    `json:"maskedRecipient"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	RowVersion      int64     `json:"rowVersion"`
	DeliveryStatus  string    `json:"deliveryStatus"`
}

type InvitationFilter struct {
	Status  string
	AfterAt *time.Time
	AfterID uuid.UUID
	Limit   int
}

type InspectedInvitation struct {
	TenantDisplayName string
	Status            string
	ExpiresAt         time.Time
}

type AcceptedInvitation struct {
	TenantID          uuid.UUID
	TenantDisplayName string
	MembershipID      uuid.UUID
	AccessPending     bool
}

type InvitationRepository interface {
	ListInvitations(context.Context, identity.RequestContext, InvitationFilter) ([]TenantInvitation, error)
	GetInvitation(context.Context, identity.RequestContext, uuid.UUID) (TenantInvitation, error)
	AuthorizeInvitationManage(context.Context, identity.RequestContext) error
	CreateInvitation(context.Context, identity.RequestContext, string, string) (TenantInvitation, error)
	CancelInvitation(context.Context, identity.RequestContext, uuid.UUID, int64) (TenantInvitation, error)
	InspectInvitation(context.Context, uuid.UUID, InvitationProof) (InspectedInvitation, error)
	AcceptExistingInvitation(context.Context, uuid.UUID, InvitationProof, string) (AcceptedInvitation, error)
}

type InvitationService struct{ repo InvitationRepository }

func NewInvitationService(repo InvitationRepository) *InvitationService {
	return &InvitationService{repo: repo}
}

func (s *InvitationService) List(ctx context.Context, rc identity.RequestContext, filter InvitationFilter) ([]TenantInvitation, error) {
	return s.repo.ListInvitations(ctx, rc, filter)
}
func (s *InvitationService) Get(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (TenantInvitation, error) {
	return s.repo.GetInvitation(ctx, rc, id)
}
func (s *InvitationService) AuthorizeManage(ctx context.Context, rc identity.RequestContext) error {
	return s.repo.AuthorizeInvitationManage(ctx, rc)
}
func (s *InvitationService) Create(ctx context.Context, rc identity.RequestContext, email, key string) (TenantInvitation, error) {
	canonical, err := NormalizeInvitationEmail(email)
	if err != nil {
		return TenantInvitation{}, err
	}
	if len(key) < 16 || len(key) > 128 {
		return TenantInvitation{}, fmt.Errorf("identity: invalid invitation key")
	}
	return s.repo.CreateInvitation(ctx, rc, canonical, key)
}
func (s *InvitationService) Cancel(ctx context.Context, rc identity.RequestContext, id uuid.UUID, version int64) (TenantInvitation, error) {
	return s.repo.CancelInvitation(ctx, rc, id, version)
}
func (s *InvitationService) Inspect(ctx context.Context, actorID uuid.UUID, code string) (InspectedInvitation, error) {
	proof, err := ParseInvitationCode(code)
	if err != nil {
		return InspectedInvitation{}, err
	}
	return s.repo.InspectInvitation(ctx, actorID, proof)
}
func (s *InvitationService) AcceptExisting(ctx context.Context, actorID uuid.UUID, code, key string) (AcceptedInvitation, error) {
	proof, err := ParseInvitationCode(code)
	if err != nil {
		return AcceptedInvitation{}, err
	}
	if len(key) < 16 || len(key) > 128 {
		return AcceptedInvitation{}, ErrInvitationKeyReused
	}
	return s.repo.AcceptExistingInvitation(ctx, actorID, proof, key)
}
