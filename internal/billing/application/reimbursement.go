package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The member's reimbursement: they paid for something themselves and want the money back
// (WP-I7-04 section 2.4, v1.2 10.10).
//
// The request itself is WP-I4-01's `REIMBURSEMENT` service request, with its own gate and its
// own eligibility. This file is the member-facing wrapper that adds what the payer's finance
// side needs beside it — the receipt, the amount, the bank reference — and the four checks
// 10.10 names.
//
// Four sentences carry it.
//
// **The IBAN never exists in plaintext anywhere but this file's local variables.** It arrives
// in the request body, it is normalised once, it goes through the platform cipher into
// `bank_account_ref_enc`, and four characters of it go into `bank_account_masked`. It is in no
// other column, no audit detail, no log line, no notification variable, no outbox payload, no
// URL and no response body. A test sweeps the schema, the audit table and the outbox for the
// test IBAN and finds only the ciphertext.
//
// **Approval consumes the member's wallet, on approval, for exactly the approved amount.** Not
// the requested amount, not at submission, and never nothing at all. The consumption, the
// claim, the decision and the outbox event are one transaction: a wallet debited while the
// decision rolled back would be a member charged for a reimbursement they never received, and a
// decision committed while the wallet stayed untouched would be money paid out of a purse
// nobody debited.
//
// **A duplicate names the earlier one.** "This is a duplicate" is not something a member can
// act on; "you were already paid for this receipt under RB-202603-XXXXXXXX on the 3rd of March"
// is something they can either accept or dispute.
//
// **KAPSORA pays nobody.** The approval hands a payment order to an adapter that, in this
// milestone, records it and does nothing (v1.2 4.3). The reference the bank gives back is typed
// in by finance afterwards, and that is what marks the row PAID.

// ReasonReimbursementApproved is the decision reason a full approval carries. A partial
// approval and a rejection carry the reviewer's own code, because those are the two a member
// appeals and an appeal is answerable only from a code somebody can count.
const ReasonReimbursementApproved = "REIMBURSEMENT_APPROVED"

// reasonEntitlementConsume is the reason code on the ledger movement, so that a member reading
// their own entitlement history sees why the balance moved.
const reasonEntitlementConsume = "REIMBURSEMENT"

// CreateReimbursementInput is the createReimbursement command.
//
// `BankAccount` is the only field in this package that ever holds an account number, and it
// holds it for the length of one function call.
type CreateReimbursementInput struct {
	// PersonID is what the body named, or uuid.Nil. The answer is the server's: the caller's
	// PERSON grant decides whose reimbursement this is, and a body naming anybody else is
	// refused by identity.RequirePerson before this input is built.
	PersonID          uuid.UUID
	ServiceRequestID  uuid.UUID
	ReceiptDocumentID uuid.UUID
	RequestedAmount   string
	CurrencyCode      string
	BankAccount       string
}

// CreateReimbursement opens the member's DRAFT reimbursement against a REIMBURSEMENT request.
//
// Everything 10.10 asks for is checked here rather than at submission, because a member who has
// typed an IBAN and pressed save should be told immediately that the receipt was already
// claimed — not after a review queue has had it for a week.
func (s *Service) CreateReimbursement(ctx context.Context, rc identity.RequestContext,
	in CreateReimbursementInput,
) (ReimbursementRecord, error) {
	ve := &domain.ValidationError{}
	amount, err := benefitdomain.ParseQuantity(in.RequestedAmount)
	switch {
	case err != nil:
		ve.Add("requestedAmount", "FORMAT", "tutar ondalık sayı olmalı")
	case !amount.IsPositive():
		ve.Add("requestedAmount", "RANGE", "tutar sıfırdan büyük olmalı")
	}
	currency := in.CurrencyCode
	if currency == "" {
		currency = defaultCurrency
	}
	if !domain.ValidCurrency(currency) {
		ve.Add("currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
	}
	if in.ServiceRequestID == uuid.Nil {
		ve.Add("serviceRequestId", "REQUIRED", "geri ödeme talebi zorunlu")
	}
	if in.ReceiptDocumentID == uuid.Nil {
		ve.Add("receiptDocumentId", "REQUIRED", "fiş ya da fatura belgesi zorunlu")
	}
	// The account number is validated in shape only. Whether it is a real account is the
	// bank's answer and nobody else's, and a platform that pretended to know would refuse
	// people whose bank it had not heard of.
	account := domain.NormalizeAccount(in.BankAccount)
	if !domain.ValidAccount(account) {
		ve.Add("bankAccount", "FORMAT", "IBAN geçerli biçimde olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return ReimbursementRecord{}, err
	}
	if s.cipher == nil {
		return ReimbursementRecord{}, fmt.Errorf("billing: no field cipher is wired")
	}

	var out ReimbursementRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		request, err := s.settlements.ReimbursementRequest(ctx, tx, rc.TenantID,
			in.ServiceRequestID, in.ReceiptDocumentID)
		if err != nil {
			return err
		}
		if !request.Found || request.RequestType != requestTypeReimbursement ||
			request.PersonID != in.PersonID {
			// One refusal for three mistakes, on purpose: telling a member which of the three
			// it was would answer a question about somebody else's data.
			return ErrRequestUnusable
		}
		if request.ProviderOrganizationID == nil {
			// `claim.claim.provider_organization_id` is NOT NULL, so an approved reimbursement
			// against a provider the directory has never heard of is a claim that cannot be
			// written. The member is told now rather than at the approval six weeks later.
			return ErrRequestUnusable
		}
		if !request.ReceiptClean {
			return ErrReceiptUnusable
		}
		if request.ServiceDefinitionID == uuid.Nil {
			return ErrRequestUnusable
		}

		// Eligibility on the *service* date, which is the day the member spent the money and
		// not the day they got round to asking.
		coverage, err := s.settlements.EnrollmentCoverage(ctx, tx, rc.TenantID,
			request.EnrollmentID, request.ServiceDate)
		if err != nil {
			return err
		}
		if !coverage.Found || coverage.PersonID != request.PersonID ||
			coverage.Status != enrollmentActive || !coverage.CoversDate {
			return ErrNotEligibleOnDate
		}

		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		if err := s.checkCeiling(ctx, tx, rc, request, amount, currency); err != nil {
			return err
		}
		if err := s.checkDuplicate(ctx, tx, rc, DuplicateProbe{
			PersonID: request.PersonID, ExcludeID: uuid.Nil,
			ReceiptSHA256:          request.ReceiptSHA256,
			ProviderOrganizationID: *request.ProviderOrganizationID,
			ServiceDate:            request.ServiceDate, RequestedAmount: amount.String(),
			WindowFrom: s.now().UTC().AddDate(0, 0, -values.ReimbursementWindowDays),
		}); err != nil {
			return err
		}

		// The one place an account number becomes something that may be stored. Two values
		// come out of it and they are taken from the same normalised string, so the mask can
		// never describe an account other than the one in the envelope.
		envelope, err := s.cipher.Encrypt(ctx, rc.TenantID, crypto.PurposeBankAccount,
			[]byte(account))
		if err != nil {
			return fmt.Errorf("billing: encrypt bank account: %w", err)
		}

		record, err := s.createReimbursementWithReference(ctx, tx, rc, NewReimbursementRow{
			PersonID: request.PersonID, EnrollmentID: request.EnrollmentID,
			ServiceRequestID:  request.ServiceRequestID,
			ReceiptDocumentID: in.ReceiptDocumentID, ReceiptSHA256: request.ReceiptSHA256,
			ServiceDefinitionID:    request.ServiceDefinitionID,
			ServiceDate:            request.ServiceDate,
			ProviderOrganizationID: *request.ProviderOrganizationID,
			RequestedAmount:        amount.String(), CurrencyCode: currency,
			BankAccountRefEnc: envelope,
			BankAccountMasked: domain.MaskAccount(account),
			ActorID:           actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}

		// The audit detail carries the mask and never the number. Four characters is what the
		// member can already see on their own screen; the envelope is not something an audit
		// log has any use for.
		if err := s.recordReimbursement(ctx, tx, rc, "reimbursement.create", record.ID,
			map[string]any{
				"reference":           record.Reference,
				"service_request_id":  record.ServiceRequestID.String(),
				"receipt_document_id": record.ReceiptDocumentID.String(),
				"service_date":        record.ServiceDate.Format(time.DateOnly),
				"requested_amount":    record.RequestedAmount,
				"currency_code":       record.CurrencyCode,
				"bank_account_masked": record.BankAccountMasked,
			}); err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return ReimbursementRecord{}, err
	}
	return out, nil
}

// requestTypeReimbursement is `service.service_request.request_type` for the request this wraps.
const requestTypeReimbursement = "REIMBURSEMENT"

// enrollmentActive is `benefit.enrollment.status` for an enrollment that is actually in force.
const enrollmentActive = "ACTIVE"

// defaultCurrency is what a body that names none means.
const defaultCurrency = "TRY"

// checkCeiling refuses a request above the contract's ceiling for the service, when the
// contract sets one. A contract that sets none is ordinary and refuses nothing.
func (s *Service) checkCeiling(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request ReimbursementRequest, amount benefitdomain.Quantity, currency string,
) error {
	var profile *uuid.UUID
	if request.ProviderOrganizationID != nil {
		id, found, err := s.settlements.ProviderProfileOf(ctx, tx, rc.TenantID,
			*request.ProviderOrganizationID)
		if err != nil {
			return err
		}
		if found {
			profile = &id
		}
	}
	ceiling, err := s.settlements.ReimbursementCeiling(ctx, tx, rc.TenantID,
		request.ServiceDefinitionID, profile, request.ServiceDate)
	if err != nil {
		return err
	}
	if ceiling == "" {
		return nil
	}
	limit := quantityOrZero(ceiling)
	if amount.Cmp(limit) <= 0 {
		return nil
	}
	return &CeilingExceededError{
		Requested: amount.String(), Ceiling: limit.String(), CurrencyCode: currency,
	}
}

// checkDuplicate refuses a repeat, naming the earlier one.
func (s *Service) checkDuplicate(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	probe DuplicateProbe,
) error {
	match, err := s.settlements.FindReimbursementDuplicate(ctx, tx, rc.TenantID, probe)
	if err != nil {
		return err
	}
	if !match.Found {
		return nil
	}
	return &DuplicateReimbursementError{
		ExistingID: match.ID, ExistingReference: match.Reference,
		ExistingDate: match.ServiceDate, ByReceipt: match.ByReceipt,
	}
}

// createReimbursementWithReference writes the header, retrying the reference on the collision
// the unique index refuses.
func (s *Service) createReimbursementWithReference(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in NewReimbursementRow,
) (ReimbursementRecord, error) {
	var lastErr error
	for attempt := 0; attempt < batchReferenceAttempts; attempt++ {
		reference, err := domain.NewReimbursementReference(s.now())
		if err != nil {
			return ReimbursementRecord{}, err
		}
		in.Reference = reference
		record, err := s.settlements.CreateReimbursement(ctx, tx, rc.TenantID, in)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, ErrReimbursementReferenceTaken) {
			return ReimbursementRecord{}, err
		}
		lastErr = err
	}
	return ReimbursementRecord{}, lastErr
}

// SubmitReimbursement sends the member's draft to the payer's finance.
func (s *Service) SubmitReimbursement(ctx context.Context, rc identity.RequestContext,
	personID, reimbursementID uuid.UUID, expected int64,
) (ReimbursementRecord, error) {
	var out ReimbursementRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		person := &personID
		record, err := s.settlements.LockReimbursement(ctx, tx, rc.TenantID, reimbursementID,
			person)
		if err != nil {
			return err
		}
		if record.Status != domain.ReimbursementDraft {
			return ErrReimbursementTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		// The duplicate check runs again here, and it is not a duplicate of the one in the
		// create. Between the two, somebody else's submission may have claimed the same
		// receipt — a household where two members hold the same account, a member who opened
		// two drafts — and the moment that matters is the moment it is actually claimed.
		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		if err := s.checkDuplicate(ctx, tx, rc, DuplicateProbe{
			PersonID: record.PersonID, ExcludeID: record.ID,
			ReceiptSHA256:          receiptDigest(ctx, tx, s, rc, record),
			ProviderOrganizationID: record.ProviderOrganizationID,
			ServiceDate:            record.ServiceDate,
			RequestedAmount:        record.RequestedAmount,
			WindowFrom:             s.now().UTC().AddDate(0, 0, -values.ReimbursementWindowDays),
		}); err != nil {
			return err
		}

		now := s.now().UTC()
		submitted, err := s.settlements.SubmitReimbursement(ctx, tx, rc.TenantID, record.ID,
			now, actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !submitted {
			return ErrVersionMismatch
		}
		if err := s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
			QueueCode: queueReimbursementReview, AggregateType: domain.AggregateReimbursement,
			AggregateID: record.ID, Title: "Geri ödeme " + record.Reference,
			ActorID: actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return err
		}
		if err := s.recordReimbursement(ctx, tx, rc, "reimbursement.submit", record.ID,
			map[string]any{
				"reference":        record.Reference,
				"requested_amount": record.RequestedAmount,
				"service_date":     record.ServiceDate.Format(time.DateOnly),
				"currency_code":    record.CurrencyCode,
			}); err != nil {
			return err
		}
		out, err = s.settlements.GetReimbursement(ctx, tx, rc.TenantID, record.ID, person)
		return err
	})
	if err != nil {
		return ReimbursementRecord{}, err
	}
	return out, nil
}

// queueReimbursementReview is the WP-I4-03 work queue a submitted reimbursement is raised into.
// A tenant that has configured no such queue raises nothing and the submit carries on, which is
// the same answer `submitBatch` gives for the same reason: a member told "your claim cannot be
// sent because the payer has not configured a work queue" has been told about somebody else's
// configuration.
const queueReimbursementReview = "REIMBURSEMENT_REVIEW"

// receiptDigest re-reads the receipt's digest for the submit's duplicate check. The record
// carries the document id rather than the digest — the digest is a column nothing renders — so
// it is read back through the same statement the create used.
//
// An unreadable receipt answers nil, which narrows the duplicate check to the
// provider-date-amount half rather than failing the submit: a document retention has purged
// since the draft was opened is not a reason to refuse a member their money.
func receiptDigest(ctx context.Context, tx pgx.Tx, s *Service, rc identity.RequestContext,
	record ReimbursementRecord,
) []byte {
	request, err := s.settlements.ReimbursementRequest(ctx, tx, rc.TenantID,
		record.ServiceRequestID, record.ReceiptDocumentID)
	if err != nil || !request.Found {
		return nil
	}
	return request.ReceiptSHA256
}

// DecideReimbursementInput is the decideReimbursement command.
type DecideReimbursementInput struct {
	Decision string
	// ApprovedAmount is meaningful on an approval. An empty string approves the whole
	// requested amount, which is what "approve" means when nobody typed a figure.
	ApprovedAmount string
	ReasonCode     string
	ReasonText     *string
}

// DecideReimbursement answers the member: approve in full, approve in part with a reason, or
// reject with a reason.
//
// An approval does four things in one transaction, and the order matters only in that all four
// commit together: it consumes exactly the approved amount from the member's money entitlement,
// it creates WP-I7-01's REIMBURSEMENT claim already decided, it moves the row to
// PAYMENT_ORDERED, and it publishes `reimbursement.approved` for the payment adapter.
//
// A rejection consumes nothing at all. That is not an omission to be tidied up later: a member
// whose receipt was refused must have the same balance afterwards as before, and a test asserts
// it by conservation.
func (s *Service) DecideReimbursement(ctx context.Context, rc identity.RequestContext,
	reimbursementID uuid.UUID, in DecideReimbursementInput, expected int64,
) (ReimbursementRecord, error) {
	ve := &domain.ValidationError{}
	if in.Decision != domain.DecisionApproveReimbursement &&
		in.Decision != domain.DecisionRejectReimbursement {
		ve.Add("decision", "ENUM", "karar APPROVE ya da REJECT olmalı")
	}
	if in.ReasonCode != "" && !domain.ValidReasonCode(in.ReasonCode) {
		ve.Add("reasonCode", "FORMAT", "gerekçe kodu BÜYÜK_HARF biçiminde olmalı")
	}
	if in.ReasonText != nil && !domain.ValidCancelReasonText(*in.ReasonText) {
		ve.Add("reasonText", "MAX_LENGTH", "gerekçe metni en fazla 1000 karakter olabilir")
	}
	if in.Decision == domain.DecisionRejectReimbursement && in.ReasonCode == "" {
		ve.Add("reasonCode", "REQUIRED", "ret için gerekçe kodu zorunlu")
	}
	if err := ve.OrNil(); err != nil {
		return ReimbursementRecord{}, err
	}

	var out ReimbursementRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		record, err := s.settlements.LockReimbursement(ctx, tx, rc.TenantID, reimbursementID,
			nil)
		if err != nil {
			return err
		}
		if record.Status != domain.ReimbursementSubmitted &&
			record.Status != domain.ReimbursementUnderReview {
			return ErrReimbursementTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		requested := quantityOrZero(record.RequestedAmount)
		approved := requested
		if in.ApprovedAmount != "" {
			approved, err = benefitdomain.ParseQuantity(in.ApprovedAmount)
			if err != nil {
				return fieldError("approvedAmount", "FORMAT", "tutar ondalık sayı olmalı")
			}
		}
		row := DecideReimbursementRow{
			DecidedBy: rc.Principal.ActorID, DecidedAt: s.now().UTC(),
			ActorID: actorPtr(rc.Principal.ActorID),
		}
		if in.ReasonText != nil {
			row.ReasonText = in.ReasonText
		}

		switch in.Decision {
		case domain.DecisionRejectReimbursement:
			row.Status = domain.ReimbursementRejected
			row.ApprovedAmount = zero().String()
			reason := in.ReasonCode
			row.ReasonCode = &reason
		default:
			if approved.IsNegative() || !approved.IsPositive() {
				return fieldError("approvedAmount", "RANGE",
					"onaylanan tutar sıfırdan büyük olmalı; reddetmek için REJECT kullanın")
			}
			if approved.Cmp(requested) > 0 {
				return fieldError("approvedAmount", "RANGE",
					"onaylanan tutar talep edilen tutardan büyük olamaz")
			}
			row.ApprovedAmount = approved.String()
			if approved.Cmp(requested) < 0 {
				if in.ReasonCode == "" {
					return fieldError("reasonCode", "REQUIRED",
						"kısmi onay için gerekçe kodu zorunlu")
				}
				row.Status = domain.ReimbursementPartiallyApproved
				reason := in.ReasonCode
				row.ReasonCode = &reason
			} else {
				row.Status = domain.ReimbursementApproved
				reason := ReasonReimbursementApproved
				if in.ReasonCode != "" {
					reason = in.ReasonCode
				}
				row.ReasonCode = &reason
			}
		}

		// The wallet and the claim, before the row moves. Both are the payer's own money
		// leaving, and a decision written before either had succeeded would be a decision a
		// reader could see with nothing behind it.
		if row.Status != domain.ReimbursementRejected {
			request, err := s.settlements.ReimbursementRequest(ctx, tx, rc.TenantID,
				record.ServiceRequestID, record.ReceiptDocumentID)
			if err != nil {
				return err
			}
			if !request.Found {
				return ErrRequestUnusable
			}
			if err := s.entitlements.Consume(ctx, tx, rc.TenantID, EntitlementConsumption{
				PersonID: record.PersonID, EnrollmentID: record.EnrollmentID,
				ReferenceID: record.ServiceRequestID, Amount: approved.String(),
				CurrencyCode: record.CurrencyCode,
				// Derived from the reimbursement and nothing else, so a redelivered command
				// consumes once.
				Key:        "reimbursement:" + record.ID.String(),
				ReasonCode: reasonEntitlementConsume,
				ActorID:    rc.Principal.ActorID, AsOf: record.ServiceDate,
			}); err != nil {
				return err
			}
			claimID, err := s.claims.RaiseReimbursement(ctx, tx, rc.TenantID,
				actorPtr(rc.Principal.ActorID), ReimbursementClaim{
					PersonID: record.PersonID, ProgramID: request.ProgramID,
					EnrollmentID:           record.EnrollmentID,
					ServiceRequestID:       record.ServiceRequestID,
					ServiceDefinitionID:    record.ServiceDefinitionID,
					ServiceDate:            record.ServiceDate,
					ApprovedAmount:         approved.String(),
					CurrencyCode:           record.CurrencyCode,
					ProviderOrganizationID: record.ProviderOrganizationID,
					ReasonCode:             derefReason(row.ReasonCode),
				})
			if err != nil {
				return err
			}
			row.ClaimID = &claimID
		}

		decided, err := s.settlements.DecideReimbursement(ctx, tx, rc.TenantID, record.ID, row,
			expected)
		if err != nil {
			return err
		}
		if !decided {
			return ErrVersionMismatch
		}

		record.Status = row.Status
		record.ApprovedAmount = row.ApprovedAmount
		if err := s.notifyReimbursementDecided(ctx, tx, rc, record, row.ApprovedAmount,
			row.DecidedAt); err != nil {
			return err
		}

		// The payment order. The event goes to the outbox inside this transaction and the
		// adapter is handed the same order; in this milestone the adapter records it and does
		// nothing, because KAPSORA transfers no money.
		if row.Status != domain.ReimbursementRejected {
			if err := s.publishReimbursementApproved(ctx, tx, rc, record, row); err != nil {
				return err
			}
			if err := s.payments.Place(ctx, tx, rc.TenantID, PaymentOrder{
				ReimbursementID: record.ID, Reference: record.Reference,
				PersonID: record.PersonID, Amount: row.ApprovedAmount,
				CurrencyCode: record.CurrencyCode,
				// Four characters. The adapter that actually moves money reads the envelope
				// from the row under its own key; an order carrying the number would be an
				// order that put it in a queue.
				BankAccountMasked: record.BankAccountMasked,
			}); err != nil {
				return err
			}
			ordered, err := s.settlements.SetReimbursementStatus(ctx, tx, rc.TenantID,
				record.ID, domain.ReimbursementPaymentOrdered,
				[]string{domain.ReimbursementApproved, domain.ReimbursementPartiallyApproved},
				actorPtr(rc.Principal.ActorID))
			if err != nil {
				return err
			}
			if !ordered {
				return ErrReimbursementTransitionInvalid
			}
		}

		if err := s.workItems.Complete(ctx, tx, rc.TenantID, domain.AggregateReimbursement,
			record.ID, row.Status, actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		if err := s.recordReimbursement(ctx, tx, rc, "reimbursement.decide", record.ID,
			map[string]any{
				"reference":        record.Reference,
				"decision":         in.Decision,
				"status":           row.Status,
				"requested_amount": requested.String(),
				"approved_amount":  row.ApprovedAmount,
				"reason_code":      derefReason(row.ReasonCode),
				"currency_code":    record.CurrencyCode,
				"claim_created":    row.ClaimID != nil,
			}); err != nil {
			return err
		}
		out, err = s.settlements.GetReimbursement(ctx, tx, rc.TenantID, record.ID, nil)
		return err
	})
	if err != nil {
		return ReimbursementRecord{}, err
	}
	return out, nil
}

// RecordReimbursementPayment marks the row PAID with the reference the bank gave back.
//
// It is the end of the road and it is somebody typing what happened elsewhere, exactly like a
// settlement's payment record: KAPSORA moved nothing and this is the note that it was moved.
func (s *Service) RecordReimbursementPayment(ctx context.Context, rc identity.RequestContext,
	reimbursementID uuid.UUID, reference string, paidAt time.Time, expected int64,
) (ReimbursementRecord, error) {
	if !domain.ValidExternalReference(reference) {
		return ReimbursementRecord{}, fieldError("paymentReference", "FORMAT",
			"ödeme referansı harf, rakam ve . _ / - karakterlerinden oluşmalı")
	}
	if paidAt.IsZero() {
		paidAt = s.now().UTC()
	}

	var out ReimbursementRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		record, err := s.settlements.LockReimbursement(ctx, tx, rc.TenantID, reimbursementID,
			nil)
		if err != nil {
			return err
		}
		switch record.Status {
		case domain.ReimbursementApproved, domain.ReimbursementPartiallyApproved,
			domain.ReimbursementPaymentOrdered:
		default:
			return ErrReimbursementTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}
		paid, err := s.settlements.SetReimbursementPaid(ctx, tx, rc.TenantID, record.ID,
			reference, paidAt.UTC(), actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		if !paid {
			return ErrReimbursementTransitionInvalid
		}
		record.Status = domain.ReimbursementPaid
		if err := s.notifyReimbursementPaid(ctx, tx, rc, record, paidAt.UTC()); err != nil {
			return err
		}
		if err := s.publishReimbursementPaid(ctx, tx, rc, record, reference,
			paidAt.UTC()); err != nil {
			return err
		}
		// The payment reference goes in the audit detail; the account does not, beyond the
		// four characters that are already on the member's own screen.
		if err := s.recordReimbursement(ctx, tx, rc, "reimbursement.payment.record", record.ID,
			map[string]any{
				"reference":           record.Reference,
				"payment_reference":   reference,
				"approved_amount":     record.ApprovedAmount,
				"currency_code":       record.CurrencyCode,
				"bank_account_masked": record.BankAccountMasked,
			}); err != nil {
			return err
		}
		out, err = s.settlements.GetReimbursement(ctx, tx, rc.TenantID, record.ID, nil)
		return err
	})
	if err != nil {
		return ReimbursementRecord{}, err
	}
	return out, nil
}

// GetReimbursement answers one reimbursement. `personID` is the PERSON scope: the member's own
// screens pass it and another member's row is simply not found, which is the same answer as a
// row that does not exist — and it is the same answer on purpose.
func (s *Service) GetReimbursement(ctx context.Context, rc identity.RequestContext,
	personID *uuid.UUID, reimbursementID uuid.UUID,
) (ReimbursementRecord, error) {
	var out ReimbursementRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		var err error
		out, err = s.settlements.GetReimbursement(ctx, tx, rc.TenantID, reimbursementID,
			personID)
		return err
	})
	if err != nil {
		return ReimbursementRecord{}, err
	}
	return out, nil
}

// ReimbursementFilter is the listReimbursements request, before it has been validated.
type ReimbursementFilter struct {
	Cursor string
	Limit  int
	// PersonID is the PERSON scope on the member's own list, and nil on the back-office one.
	PersonID *uuid.UUID
	Status   string
	DateFrom *time.Time
	DateTo   *time.Time
}

// ListReimbursements answers one page, newest first.
func (s *Service) ListReimbursements(ctx context.Context, rc identity.RequestContext,
	f ReimbursementFilter,
) (ReimbursementPage, error) {
	ve := &domain.ValidationError{}
	query := ReimbursementQuery{
		PersonID: f.PersonID, DateFrom: f.DateFrom, DateTo: f.DateTo,
	}
	if f.Status != "" {
		if !domain.ValidReimbursementStatus(f.Status) {
			ve.Add("status", "ENUM", "tanımlı bir geri ödeme durumu olmalı")
		} else {
			status := f.Status
			query.Status = &status
		}
	}
	if err := ve.OrNil(); err != nil {
		return ReimbursementPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ReimbursementPage{}, err
	}
	query.After = after
	query.PageSize = pageSize + 1

	var out ReimbursementPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		rows, err := s.settlements.ListReimbursements(ctx, tx, rc.TenantID, query)
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(httpx.Cursor{
				CreatedAt: rows[pageSize-1].CreatedAt, ID: rows[pageSize-1].ID,
			})
			rows = rows[:pageSize]
		}
		if rows == nil {
			rows = []ReimbursementRecord{}
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return ReimbursementPage{}, err
	}
	return out, nil
}

// publishReimbursementApproved writes the outbox row an approved reimbursement owes. The
// payment adapter of M9 listens for it.
//
// There is no account number in the payload and there never will be: the adapter reads the
// envelope from the row under its own key. What travels is the four characters, so that a
// consumer can show a member which account without anybody decrypting anything.
func (s *Service) publishReimbursementApproved(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record ReimbursementRecord, row DecideReimbursementRow,
) error {
	payload := map[string]any{
		"reimbursementId":   record.ID,
		"reference":         record.Reference,
		"personId":          record.PersonID,
		"serviceRequestId":  record.ServiceRequestID,
		"approvedAmount":    row.ApprovedAmount,
		"currencyCode":      record.CurrencyCode,
		"bankAccountMasked": record.BankAccountMasked,
		"status":            row.Status,
	}
	if row.ClaimID != nil {
		payload["claimId"] = *row.ClaimID
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateReimbursement, AggregateID: record.ID,
		Type: ReimbursementApprovedEvent, Payload: payload,
		// The reimbursement's own id: one is approved once, and a redelivered command that
		// moved nothing publishes no second event either.
		DeduplicationKey: record.ID.String(),
	})
	return err
}

// publishReimbursementPaid records that the money has actually gone, for whichever reconciler
// wants it. Same rule about the account: four characters, never the number.
func (s *Service) publishReimbursementPaid(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record ReimbursementRecord, reference string, paidAt time.Time,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateReimbursement, AggregateID: record.ID,
		Type: ReimbursementPaidEvent,
		Payload: map[string]any{
			"reimbursementId":   record.ID,
			"reference":         record.Reference,
			"personId":          record.PersonID,
			"amount":            record.ApprovedAmount,
			"currencyCode":      record.CurrencyCode,
			"paymentReference":  reference,
			"paidAt":            paidAt.Format(time.RFC3339),
			"bankAccountMasked": record.BankAccountMasked,
		},
		DeduplicationKey: record.ID.String() + ":paid",
	})
	return err
}

// recordReimbursement writes one business audit row about a reimbursement.
//
// Everything that reaches a detail map here is an id, a code, a status, a date, an exact
// decimal or the four characters of the mask. The account number never does — not the value and
// not the envelope — because an audit log is read by more people than any screen in this
// product.
//
// Every key is snake_case, because `audit.SanitizeDetail` keeps only keys matching
// `^[a-z][a-z0-9_]*$` and drops everything else silently.
func (s *Service) recordReimbursement(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, action string, reimbursementID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateReimbursement,
		ResourceID: nullUUID(reimbursementID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// derefReason renders an optional reason code for an audit detail. The empty string is "no
// reason", which is what a full approval carries.
func derefReason(code *string) string {
	if code == nil {
		return ""
	}
	return *code
}
