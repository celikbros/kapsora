package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

var ErrMembershipNotFound = errors.New("identity: tenant membership not found")

// DirectoryMembership is deliberately a tenant membership, never a global actor profile.
type DirectoryMembership struct {
	ID               uuid.UUID
	DisplayName      string
	ActorType        string
	ActorStatus      string
	MembershipStatus string
	ValidFrom        *string
	ValidTo          *string
	ValidityEmpty    bool
	CreatedAt        time.Time
	RowVersion       int64
}

// AssignedRole reports assignments, including future and expired ones. It does not claim
// that a role is currently effective, and intentionally omits scope identifiers and reasons.
type AssignedRole struct {
	Code          string
	Name          string
	System        bool
	ScopeType     string
	ValidFrom     *string
	ValidTo       *string
	ValidityEmpty bool
}

type DirectoryDetail struct {
	Membership    DirectoryMembership
	AssignedRoles []AssignedRole
}

type DirectoryFilter struct {
	Status string
	After  *httpx.Cursor
	Limit  int
}

type DirectoryRepository interface {
	List(ctx context.Context, rc identity.RequestContext, filter DirectoryFilter) ([]DirectoryMembership, error)
	Get(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID) (DirectoryDetail, error)
	AuthorizeManage(ctx context.Context, rc identity.RequestContext) error
	Suspend(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, expectedVersion int64, reasonCode string) (DirectoryDetail, error)
}

type DirectoryService struct {
	repo    DirectoryRepository
	cursors *httpx.CursorCodec
}

func NewDirectoryService(repo DirectoryRepository, cursors *httpx.CursorCodec) *DirectoryService {
	return &DirectoryService{repo: repo, cursors: cursors}
}

type DirectoryPage struct {
	Items      []DirectoryMembership
	NextCursor string
}

func (s *DirectoryService) List(ctx context.Context, rc identity.RequestContext, status, cursor string, limit int) (DirectoryPage, error) {
	if s.cursors == nil {
		return DirectoryPage{}, errors.New("identity: directory cursor unavailable")
	}
	decoded, present, err := s.cursors.Decode(cursor)
	if err != nil {
		return DirectoryPage{}, err
	}
	pageSize := httpx.ClampLimit(limit)
	filter := DirectoryFilter{Status: status, Limit: pageSize + 1}
	if present {
		filter.After = &decoded
	}
	items, err := s.repo.List(ctx, rc, filter)
	if err != nil {
		return DirectoryPage{}, err
	}
	page := DirectoryPage{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

func (s *DirectoryService) Get(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID) (DirectoryDetail, error) {
	return s.repo.Get(ctx, rc, membershipID)
}
