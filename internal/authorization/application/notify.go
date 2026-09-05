package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// ExpiringNoticeDays is how long before an authorization runs out the member is told.
// Three days is not a technical number: it is enough time to use a promise or to ask for
// an extension, and short enough that the reminder is about this week.
const ExpiringNoticeDays = 3

// ExpiringBatchSize bounds one pass of the reminder sweep per tenant.
const ExpiringBatchSize = 500

// notifyApproved tells the member their authorization exists, inside the transaction that
// created it. It carries the reference, the day the promise runs out and a link — no
// amount, no line, no service name, because what was authorized is on the screen the link
// points at and an e-mail is not a place to put it.
func (s *Service) notifyApproved(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record AuthorizationRecord, personID uuid.UUID,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode:  notificationapp.EventAuthorizationApproved,
		Key:        record.ID.String(),
		Recipients: []notificationapp.Recipient{notificationapp.PersonRecipient(personID)},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  domainStatusApproved,
			notificationdomain.VarEventDate:   record.ApprovedAt.UTC().Format(time.DateOnly),
			notificationdomain.VarExpiresAt:   record.ValidTo.UTC().Format(time.DateOnly),
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("authorizations", record.ID),
		},
	})
}

// domainStatusApproved is the status word an approval notification carries. The
// authorization's own status is ACTIVE, which is true and says nothing: what happened is
// that somebody approved it.
const domainStatusApproved = "APPROVED"

// NotifyExpiringAuthorizations publishes the reminder for every authorization whose
// validity ends ExpiringNoticeDays from now, tenant by tenant. It is the body of the
// authorization.expiring scheduler job.
//
// Running it twice tells nobody twice. The deduplication key carries the authorization
// and the day it expires — facts, not the moment the sweep ran — so an hourly job writes
// one message on the first pass of that day and nothing on the other twenty-three.
func (s *Service) NotifyExpiringAuthorizations(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	// The window is the whole UTC day ExpiringNoticeDays ahead: an authorization ending
	// at 09:00 and one ending at 23:00 are both "expiring on Thursday", and a member does
	// not think in hours about a promise that lasts weeks.
	target := now.UTC().AddDate(0, 0, ExpiringNoticeDays).Truncate(24 * time.Hour)
	published := 0
	for _, tenantID := range tenants {
		count, err := s.notifyExpiringTenant(ctx, tenantID, target)
		if err != nil {
			return published, err
		}
		published += count
	}
	return published, nil
}

func (s *Service) notifyExpiringTenant(ctx context.Context, tenantID uuid.UUID, dayStart time.Time) (int, error) {
	count := 0
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			rows, err := s.repo.ListExpiringAuthorizations(ctx, tx, tenantID,
				dayStart, dayStart.AddDate(0, 0, 1), ExpiringBatchSize)
			if err != nil {
				return err
			}
			for _, row := range rows {
				expires := row.ValidTo.UTC().Format(time.DateOnly)
				if err := notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
					EventCode:  notificationapp.EventAuthorizationExpiring,
					Key:        row.ID.String() + ":" + expires,
					Recipients: []notificationapp.Recipient{notificationapp.PersonRecipient(row.PersonID)},
					Variables: map[string]string{
						notificationdomain.VarReferenceNo: row.Reference,
						notificationdomain.VarExpiresAt:   expires,
						notificationdomain.VarDeepLink:    notificationapp.DeepLink("authorizations", row.ID),
					},
				}); err != nil {
					return err
				}
				count++
			}
			return nil
		})
	if err != nil {
		return 0, fmt.Errorf("authorization: expiring notices for tenant %s: %w", tenantID, err)
	}
	return count, nil
}
