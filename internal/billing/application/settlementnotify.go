package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
)

// The four messages WP-I7-04 sends, published inside the command's own transaction.
//
// They write outbox rows and nothing else: an approval that rolls back has notified nobody, and
// a notification can neither slow a payment down nor make one fail.
//
// **What they carry is the safe-variable catalogue and nothing else.** A reference, a status
// word, a day, an amount, a currency, a link — and, in exactly one of them, four characters of
// a bank account.
//
// That last one is worth being explicit about, because it is the whole privacy claim of this
// package. `reimbursement.paid` tells a member which account the money went to, and the only
// form of that answer this platform holds is `bank_account_masked`: four characters, written by
// `domain.MaskAccount`, CHECKed by the column and CHECKed again by the catalogue's own rule.
// The IBAN itself exists as one ciphertext in one column and reaches no message, no payload, no
// audit detail and no response body. `reimbursement.decided` carries no account detail at all —
// a member being told a figure has no need to be told their own account number back.

// notifySettlementApproved tells the provider that what the payer owes has been released.
//
// The amount is the payable figure and not the approved total, because that is what is actually
// going to arrive; what was withheld is on the screen the link leads to, beside the recoveries
// that explain it.
func (s *Service) notifySettlementApproved(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record SettlementRecord, now time.Time,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventSettlementApproved,
		// The settlement and the fact: one settlement is approved once, so a redelivered
		// command produces the same key and the message table refuses the second copy.
		Key: record.ID.String() + ":" + domain.SettlementApproved,
		Recipients: []notificationapp.Recipient{
			notificationapp.OrganizationRecipient(record.ProviderOrganizationID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  domain.SettlementApproved,
			notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
			notificationdomain.VarExpiresAt:   record.DueDate.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:      record.PayableAmount,
			notificationdomain.VarCurrency:    record.CurrencyCode,
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("settlements", record.ID),
		},
	})
}

// notifyPaymentRecorded tells the provider that a payment has been entered against its
// settlement.
//
// The external reference is deliberately absent. It is a bank's own transaction identifier,
// which is the kind of value that turns up in a phishing message quoted back at somebody as
// proof of legitimacy; the settlement's own reference is what the provider needs in order to
// find the row, and the bank reference is on the screen behind a sign-in.
func (s *Service) notifyPaymentRecorded(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record SettlementRecord, payment PaymentRecordRecord,
	status string,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventPaymentRecorded,
		// The payment record's own id: one record is entered once, and a redelivered command
		// that wrote nothing notifies nobody a second time.
		Key: payment.ID.String(),
		Recipients: []notificationapp.Recipient{
			notificationapp.OrganizationRecipient(record.ProviderOrganizationID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  status,
			notificationdomain.VarEventDate:   payment.PaidAt.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:      payment.Amount,
			notificationdomain.VarCurrency:    payment.CurrencyCode,
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("settlements", record.ID),
		},
	})
}

// notifyReimbursementDecided tells the member what was decided about their receipt.
//
// The amount is what was approved — nought on a rejection, which is the honest figure — and the
// status word is what separates the three answers. There is no bank detail of any kind: a
// member being told a decision has no need to be told their own account number back, and the
// message that does carry the mask is the one about money that has actually moved.
func (s *Service) notifyReimbursementDecided(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record ReimbursementRecord, approved string, now time.Time,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventReimbursementDecided,
		Key:       record.ID.String() + ":" + record.Status,
		Recipients: []notificationapp.Recipient{
			notificationapp.PersonRecipient(record.PersonID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  record.Status,
			notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:      approved,
			notificationdomain.VarCurrency:    record.CurrencyCode,
			notificationdomain.VarDeepLink: notificationapp.DeepLink("reimbursements",
				record.ID),
		},
	})
}

// notifyReimbursementPaid tells the member the money has gone, and to which account.
//
// `masked_account` is the only variable in the catalogue that carries anything about a bank
// account, its rule admits exactly four upper-case alphanumerics, and the value is the four
// characters `domain.MaskAccount` produced when the member typed the IBAN. Nothing else about
// the account exists outside `bank_account_ref_enc`.
func (s *Service) notifyReimbursementPaid(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record ReimbursementRecord, paidAt time.Time,
) error {
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventReimbursementPaid,
		Key:       record.ID.String() + ":" + domain.ReimbursementPaid,
		Recipients: []notificationapp.Recipient{
			notificationapp.PersonRecipient(record.PersonID),
		},
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:   record.Reference,
			notificationdomain.VarStatusCode:    domain.ReimbursementPaid,
			notificationdomain.VarEventDate:     paidAt.UTC().Format(time.DateOnly),
			notificationdomain.VarAmount:        record.ApprovedAmount,
			notificationdomain.VarCurrency:      record.CurrencyCode,
			notificationdomain.VarMaskedAccount: record.BankAccountMasked,
			notificationdomain.VarDeepLink: notificationapp.DeepLink("reimbursements",
				record.ID),
		},
	})
}
