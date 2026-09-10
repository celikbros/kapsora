package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The claim an approved reimbursement creates (WP-I7-04 section 2.4), and the third command in
// this package that runs inside somebody else's transaction.
//
// It lives here for the reason `invoiced.go` and `invoicecut.go` give: what a claim is, what a
// decision on a line is and which statuses either may reach are this module's rules, and a
// billing package that inserted `claim.claim` rows itself would be a second implementation of
// all three. WP-I7-04 owns *when* the claim exists — at the approval, never before — and
// everything about *what* it is, is below.
//
// Three sentences carry it.
//
// **The claim is created already decided.** The payer has just decided it: the reviewer typed
// an amount into `decideReimbursement` and pressed approve, and asking a second reviewer to
// decide the same money again would be asking the same question twice. The line is billed at
// the approved amount and approved at the approved amount in the same transaction, by the
// actor who decided the reimbursement, at the FINANCIAL stage — because that is who decided it
// and that is which stage they decided it in.
//
// **It is the member's money, not the provider's.** `payer_amount` is the whole approved
// amount and `member_amount` is nought: the member has already paid the provider, and what
// this claim records is the payer owing the member. `provider_organization_id` names who was
// paid, because `claim.claim` requires it and because "who did this member spend the money
// with" is the question every report about reimbursements asks first.
//
// **There is no adjustment row.** Approving less than was requested is not a cut: nothing was
// ever billed at the requested figure, and the difference between "asked for 500" and
// "approved 300" lives on `billing.reimbursement` where the member can see both. A zero-value
// or phantom row in `claim.adjustment` would be noise in the one table that must only ever
// hold money that moved.

// UnitReimbursement is the unit type of a reimbursement claim's single line. It is one event —
// one receipt, one payment the member made — which is why its quantity is always exactly one.
const UnitReimbursement = "REIMBURSEMENT"

// ReasonReimbursementApproved is why the line is already approved when the claim is created:
// the decision was taken in `decideReimbursement`, and the claim is recording it rather than
// making it again.
const ReasonReimbursementApproved = "REIMBURSEMENT_APPROVED"

// ReimbursementClaimInput is one approved reimbursement, in this module's vocabulary.
type ReimbursementClaimInput struct {
	PersonID            uuid.UUID
	ProgramID           uuid.UUID
	EnrollmentID        uuid.UUID
	ServiceRequestID    uuid.UUID
	ServiceDefinitionID uuid.UUID
	ServiceDate         time.Time
	// ApprovedAmount is what the payer agreed to pay the member back, exact decimal text.
	ApprovedAmount         string
	CurrencyCode           string
	ProviderOrganizationID uuid.UUID
	// ReasonCode is why, when the approval was partial. An empty string is a full approval and
	// the line carries ReasonReimbursementApproved.
	ReasonCode string
	DecidedBy  *uuid.UUID
}

// RaiseReimbursementClaim writes the whole claim, in the order the schema requires, inside the
// caller's transaction.
//
// A second call for the same request finds the claim the first one made and answers with its
// id rather than writing a second: `decideReimbursement` is guarded by a row version and a
// status, so this cannot ordinarily happen, and "ordinarily" is not a guarantee anybody should
// rely on for money.
func (s *Service) RaiseReimbursementClaim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, in ReimbursementClaimInput,
) (uuid.UUID, error) {
	rc := systemContext(tenantID)
	existing, found, err := s.repo.FindLiveClaimBySource(ctx, tx, tenantID,
		domain.SourceReimbursement, in.ServiceRequestID)
	if err != nil {
		return uuid.Nil, err
	}
	if found {
		return existing, nil
	}

	sourceType := domain.SourceReimbursement
	sourceID := in.ServiceRequestID
	day := domain.DateOnly(in.ServiceDate)
	record, err := s.createSourceClaim(ctx, tx, rc, NewClaimRow{
		PersonID: in.PersonID, ProgramID: in.ProgramID, EnrollmentID: in.EnrollmentID,
		ProviderOrganizationID: in.ProviderOrganizationID,
		DomainCode:             domain.DomainGeneric,
		SourceType:             &sourceType, SourceID: &sourceID,
		ServiceDateFrom: day, ServiceDateTo: day,
		Channel: domain.DefaultChannel, ActorID: actorID,
	})
	if err != nil {
		return uuid.Nil, err
	}
	version, err := s.repo.CreateVersion(ctx, tx, tenantID, record.ID, 1, actorID)
	if err != nil {
		return uuid.Nil, err
	}
	amount := in.ApprovedAmount
	currency := in.CurrencyCode
	if err := s.repo.ReplaceLines(ctx, tx, tenantID, version.ID, lineRows(version.ID,
		[]NewLineInput{{
			LineNo: 1, ServiceDefinitionID: in.ServiceDefinitionID,
			UnitType: UnitReimbursement, Quantity: "1", UnitAmount: &amount,
			LineAmount: amount, CurrencyCode: &currency,
		}}, actorID)); err != nil {
		return uuid.Nil, err
	}
	lines, err := s.repo.ListLines(ctx, tx, tenantID, version.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(lines) != 1 {
		return uuid.Nil, ErrLineNotFound
	}

	// The decision the payer has just taken, recorded rather than taken again. The stage is
	// FINANCIAL and the actor is the reviewer who approved the reimbursement, because that is
	// literally what happened; a claim marked AUTO here would say a machine decided money a
	// person released.
	reason := ReasonReimbursementApproved
	if in.ReasonCode != "" {
		reason = in.ReasonCode
	}
	now := s.now().UTC()
	if _, err := s.repo.CreateDecision(ctx, tx, tenantID, NewDecisionRow{
		LineID: lines[0].ID, DecidedInVersionNo: version.VersionNo,
		Decision: domain.DecisionApproved, ApprovedQuantity: "1",
		ApprovedAmount: amount, PayerAmount: amount, MemberAmount: zero().String(),
		ReasonCode: reason, DecidedBy: actorID, DecidedAt: now,
		Stage: domain.StageFinancial,
	}); err != nil {
		return uuid.Nil, err
	}

	snapshot, err := sourceClaimSnapshot(record, version, lines, in.CurrencyCode)
	if err != nil {
		return uuid.Nil, err
	}
	frozen, err := s.repo.FreezeVersion(ctx, tx, tenantID, version.ID, FreezeRow{
		Snapshot: snapshot, SubmittedAt: now,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if !frozen {
		return uuid.Nil, ErrTransitionInvalid
	}

	// Straight to APPROVED. There is no stage still owed: a reimbursement has no medical
	// review — the member has already had the service — and its financial review is the
	// decision that created this claim.
	moved, err := s.repo.SetStatus(ctx, tx, tenantID, record.ID, StatusRow{
		Status: domain.StatusApproved, CurrentVersionNo: version.VersionNo,
		FromStatuses: []string{domain.StatusDraft},
	}, record.RowVersion)
	if err != nil {
		return uuid.Nil, err
	}
	if !moved {
		return uuid.Nil, ErrTransitionInvalid
	}
	return record.ID, nil
}
