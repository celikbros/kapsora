package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
)

// The two messages an icmal sends, published inside the command's own transaction.
//
// They write outbox rows and nothing else: a submit that rolls back has notified nobody, and a
// notification can neither slow a decision down nor make one fail.
//
// **What they carry is six of the safe variables and nothing else.** The reference, the
// provider's display name, the day, the total, the currency, a status word and a link. There is
// no invoice number in either — a provider's fiscal document numbers are not something to
// broadcast — and no reason text, which is free prose a reviewer typed. Both are on the screen
// the deep link leads to, behind a sign-in, which is where they belong.
//
// A batch whose payer is the tenant itself has no organization row to address, so the submitted
// message reaches nobody. That is the honest answer rather than a failure: the work item is
// what puts such a batch in front of the payer's finance department, and it was raised in the
// same transaction.

// notifyBatchSubmitted tells the payer's finance that an icmal is waiting.
func (s *Service) notifyBatchSubmitted(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record BatchRecord, total benefitdomain.Quantity, now time.Time,
) error {
	if record.PayerOrganizationID == nil {
		return nil
	}
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBatchSubmitted,
		// The batch and the fact: one icmal is submitted once, so a redelivered command
		// produces the same key and the message table refuses the second copy.
		Key: record.ID.String() + ":" + domain.BatchSubmitted,
		Recipients: []notificationapp.Recipient{
			notificationapp.OrganizationRecipient(*record.PayerOrganizationID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarProviderName: record.ProviderName,
			notificationdomain.VarEventDate:    now.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:       total.String(),
			notificationdomain.VarCurrency:     record.CurrencyCode,
			notificationdomain.VarDeepLink:     notificationapp.DeepLink("batches", record.ID),
		},
	})
}

// notifyBatchDecided tells the provider what the payer answered.
//
// The amount is the approved total and not the submitted one, because that is the figure the
// provider is going to be paid; the rest of the arithmetic — what was cut, returned and
// rejected — is on the summary the link leads to.
func (s *Service) notifyBatchDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BatchRecord, totals domain.BatchTotals, now time.Time,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBatchDecided,
		Key:       record.ID.String() + ":" + domain.BatchDecided,
		Recipients: []notificationapp.Recipient{
			notificationapp.OrganizationRecipient(record.ProviderOrganizationID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  domain.BatchDecided,
			notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:      totals.Approved.String(),
			notificationdomain.VarCurrency:    record.CurrencyCode,
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("batches", record.ID),
		},
	})
}
