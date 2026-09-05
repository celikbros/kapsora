package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The status words a decision notification carries. They are the request's own statuses
// except for a return, which lands on DRAFT — a status word of "DRAFT" would tell the
// member their request had been forgotten rather than sent back for a correction.
const statusReturned = "RETURNED"

// notifyDecided tells the member and the provider that somebody has answered.
//
// It runs inside the command's transaction and writes outbox rows and nothing else: a
// command that rolls back has notified nobody, and a notification can neither slow a
// decision down nor make one fail. What it carries is a reference, a status word, a day
// and a link — never a reason text, never a comment, never a line of the request. The
// safe-variable catalogue would refuse any of those, and the point of only ever passing
// these four is that it never has to.
func (s *Service) notifyDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord, statusCode string,
) error {
	recipients := []notificationapp.Recipient{notificationapp.PersonRecipient(request.PersonID)}
	if request.ProviderOrganizationID != nil {
		recipients = append(recipients, notificationapp.OrganizationRecipient(*request.ProviderOrganizationID))
	}
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventServiceRequestDecided,
		// The request and the status it landed on: replaying the same decision produces
		// the same key, and two different decisions on one request are two events.
		Key:        request.ID.String() + ":" + statusCode,
		Recipients: recipients,
		Variables:  decisionVariables(request, statusCode, s.now()),
	})
}

// notifyPendingDocument asks the provider for what the gate is missing. The member is not
// told: nothing is being asked of them, and a notification saying "your request is
// waiting for a document" that they cannot upload is a notification that only worries.
func (s *Service) notifyPendingDocument(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord,
) error {
	if request.ProviderOrganizationID == nil {
		return nil
	}
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode:  notificationapp.EventServiceRequestPendingDocument,
		Key:        request.ID.String(),
		Recipients: []notificationapp.Recipient{notificationapp.OrganizationRecipient(*request.ProviderOrganizationID)},
		Variables: decisionVariables(request,
			servicerequestdomain.StatusPendingDocument, s.now()),
	})
}

// decisionVariables is the whole of what a request notification may say.
func decisionVariables(request RequestRecord, statusCode string, now time.Time) map[string]string {
	return map[string]string{
		notificationdomain.VarReferenceNo: request.Reference,
		notificationdomain.VarStatusCode:  statusCode,
		notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
		notificationdomain.VarDeepLink:    notificationapp.DeepLink("requests", request.ID),
	}
}
