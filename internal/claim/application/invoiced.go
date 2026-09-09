package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The two transitions an invoice causes, and the only two commands in this package that run
// inside somebody else's transaction.
//
// They live here rather than in the billing module because the transition is this module's
// rule: **a claim moves to INVOICED only from a decision the payer actually took**, and it
// comes back only to the decision it came from. WP-I7-02 owns *when* it happens — a submit, a
// cancel, a return — and everything about *whether* it may is below.
//
// Neither of them opens a transaction. They are given the caller's, so a claim that moved to
// INVOICED and an invoice that failed to submit is a state no reader ever observes, and an
// invoice whose claims could not be moved does not submit at all.
//
// Neither writes an audit row either. The audit of this fact is the invoice's — one row saying
// which document collected which claims — and a second row per claim would be a hundred rows
// saying the same thing in a log somebody has to read.

// invoiceableFrom are the statuses a claim may be put on an invoice from. They are the two the
// payer has answered; anything else is money being collected for a decision nobody took.
var invoiceableFrom = []string{domain.StatusApproved, domain.StatusPartiallyApproved}

// MarkInvoiced moves claims onto an invoice, inside the caller's transaction.
//
// Every claim has to move. A submit that moved four of five claims would leave the fifth
// invoiceable while an invoice was already collecting it, which is exactly how a provider
// comes to be paid twice — so a claim that is no longer in an invoiceable status fails the
// whole command and the invoice does not submit.
func (s *Service) MarkInvoiced(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	for _, id := range claims {
		moved, err := s.repo.SetInvoiceStatus(ctx, tx, tenantID, id,
			domain.StatusInvoiced, invoiceableFrom, actorID)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("%w: %s", ErrTransitionInvalid, id)
		}
	}
	return nil
}

// ReleaseFromInvoice puts each claim back to the status it carried when it was allocated.
//
// The status is the invoice's record of it rather than something recomputed here, for the
// reason WP-I7-02's schema comment gives: "was this fully or partly approved" is a question
// this module already answered when it decided the claim, and answering it a second time from
// the line decisions would be a second implementation that could disagree.
//
// A claim that is not INVOICED is skipped rather than failing. Cancelling an invoice must
// always be possible: a claim a settlement has already moved on to BATCHED is not this
// command's to drag backwards, and refusing the whole cancellation over it would leave a
// provider holding an invoice they cannot withdraw.
func (s *Service) ReleaseFromInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, releases []ClaimRelease,
) error {
	for _, release := range releases {
		status := release.Status
		if status != domain.StatusApproved && status != domain.StatusPartiallyApproved {
			// An invoice that recorded something else recorded something this module never
			// wrote. The safe reading is the narrower one: a partially approved claim is
			// never wrongly restored to fully approved.
			status = domain.StatusPartiallyApproved
		}
		if _, err := s.repo.SetInvoiceStatus(ctx, tx, tenantID, release.ClaimID,
			status, []string{domain.StatusInvoiced}, actorID); err != nil {
			return err
		}
	}
	return nil
}

// ClaimRelease is one claim going back where it came from.
type ClaimRelease struct {
	ClaimID uuid.UUID
	// Status is what the claim was when it was allocated.
	Status string
}
