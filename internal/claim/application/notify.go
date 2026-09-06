package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
)

// notifyDecided is `claim.decided`, which WP-I5-05 registered a template and recipients for
// and left without a publisher. This is the publisher.
//
// It runs inside the deciding command's transaction and writes outbox rows and nothing else: a
// decision that rolls back has notified nobody, and a notification can neither slow a decision
// down nor make one fail.
//
// **What it carries is six of the ten safe variables and nothing else.** The reference, the
// status word, the day, the approved total, the currency and a link. There is no slot in the
// template for a diagnosis, a line description, a reviewer's comment or a reason text — and
// the point of only ever passing these six is that the renderer never has to refuse one. The
// amount is exact decimal text the whole way; a total that had passed through a float would be
// a total the member and the provider read differently.
func (s *Service) notifyDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, statusCode string, totals pipelineTotals, currency string, now time.Time,
) error {
	recipients := []notificationapp.Recipient{
		notificationapp.PersonRecipient(record.PersonID),
		notificationapp.OrganizationRecipient(record.ProviderOrganizationID),
	}
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventClaimDecided,
		// The claim and the status it landed on: replaying the same decision produces the
		// same key, and the message table refuses the second copy.
		Key:        record.ID.String() + ":" + statusCode,
		Recipients: recipients,
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  statusCode,
			notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:      totals.Approved.String(),
			notificationdomain.VarCurrency:    currency,
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("claims", record.ID),
		},
	})
}
