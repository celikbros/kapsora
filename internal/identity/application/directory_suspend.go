package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
)

var (
	ErrDirectoryReasonInvalid   = errors.New("identity: invalid suspension reason")
	ErrDirectorySelfSuspension  = errors.New("identity: cannot suspend own tenant membership")
	ErrDirectoryLastManager     = errors.New("identity: cannot suspend last usable tenant manager")
	ErrDirectoryStateConflict   = errors.New("identity: tenant membership is not active")
	ErrDirectoryVersionConflict = errors.New("identity: tenant membership version changed")
)

func ValidDirectorySuspensionReason(reason string) bool {
	switch reason {
	case "ACCESS_REVIEW", "STAFF_DEPARTURE", "SECURITY_CONCERN":
		return true
	default:
		return false
	}
}

func (s *DirectoryService) AuthorizeManage(ctx context.Context, rc identity.RequestContext) error {
	return s.repo.AuthorizeManage(ctx, rc)
}

func (s *DirectoryService) Suspend(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, expectedVersion int64, reasonCode string) (DirectoryDetail, error) {
	if !ValidDirectorySuspensionReason(reasonCode) {
		return DirectoryDetail{}, ErrDirectoryReasonInvalid
	}
	if expectedVersion < 1 {
		return DirectoryDetail{}, ErrDirectoryVersionConflict
	}
	return s.repo.Suspend(ctx, rc, membershipID, expectedVersion, reasonCode)
}
