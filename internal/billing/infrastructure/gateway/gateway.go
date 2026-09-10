// Package billinggw is where the invoice meets the one module it cannot do without: the
// claim.
//
// It exists so that the claim module does not know an invoice exists and the billing module
// holds no copy of what a claim status means. The port is declared in billing/application in
// that package's own vocabulary — mark these invoiced, put these back — and the adapter below
// is the only code in the repository that speaks both languages.
//
// Nothing here decides anything. Whether a claim may move to INVOICED is the claim module's
// answer, and an adapter that added a rule would be a rule nobody reading either module could
// find.
package billinggw

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	billingapp "github.com/celikbros/kapsora/internal/billing/application"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
)

// Claims is the claim module seen from the invoice: move these onto a document, and put these
// back where they came from.
type Claims struct{ svc *claimapp.Service }

// NewClaims wraps the claim module.
func NewClaims(svc *claimapp.Service) *Claims { return &Claims{svc: svc} }

var _ billingapp.ClaimsPort = (*Claims)(nil)

// MarkInvoiced implements billingapp.ClaimsPort. It runs in the invoice's own transaction,
// which is what makes "the invoice submitted and its claims did not move" a state no reader
// observes.
func (c *Claims) MarkInvoiced(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	return c.svc.MarkInvoiced(ctx, tx, tenantID, actorID, claims)
}

// ReleaseFromInvoice implements billingapp.ClaimsPort.
func (c *Claims) ReleaseFromInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, releases []billingapp.ClaimRelease,
) error {
	out := make([]claimapp.ClaimRelease, 0, len(releases))
	for _, release := range releases {
		out = append(out, claimapp.ClaimRelease{
			ClaimID: release.ClaimID, Status: release.Status,
		})
	}
	return c.svc.ReleaseFromInvoice(ctx, tx, tenantID, actorID, out)
}

// The icmal's half of the boundary (WP-I7-03). The batch decides *when* a claim is cut, put
// back or closed unpaid; the claim module decides *whether* it may, and every one of these
// runs inside the batch command's own transaction.

// Cut implements billingapp.ClaimsPort.
func (c *Claims) Cut(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
	cuts []billingapp.ClaimCut,
) ([]billingapp.ClaimAdjustment, error) {
	in := make([]claimapp.InvoiceCut, 0, len(cuts))
	for _, cut := range cuts {
		in = append(in, claimapp.InvoiceCut{
			ClaimID: cut.ClaimID, Amount: cut.Amount,
			ReasonCode: cut.ReasonCode, ReasonText: cut.ReasonText,
		})
	}
	written, err := c.svc.CutAcrossClaims(ctx, tx, tenantID, actorID, in)
	if err != nil {
		return nil, err
	}
	return adjustmentsOf(written), nil
}

// Reverse implements billingapp.ClaimsPort.
func (c *Claims) Reverse(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
	reversals []billingapp.ClaimReversal,
) ([]billingapp.ClaimAdjustment, error) {
	in := make([]claimapp.InvoiceReversal, 0, len(reversals))
	for _, reversal := range reversals {
		in = append(in, claimapp.InvoiceReversal{
			ClaimID: reversal.ClaimID, AdjustmentID: reversal.AdjustmentID,
			ReasonText: reversal.ReasonText,
		})
	}
	written, err := c.svc.ReverseInvoiceCuts(ctx, tx, tenantID, actorID, in)
	if err != nil {
		return nil, err
	}
	return adjustmentsOf(written), nil
}

// CloseUnpaid implements billingapp.ClaimsPort.
func (c *Claims) CloseUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	return c.svc.CloseUnpaid(ctx, tx, tenantID, actorID, claims)
}

// ReopenUnpaid implements billingapp.ClaimsPort.
func (c *Claims) ReopenUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	return c.svc.ReopenUnpaid(ctx, tx, tenantID, actorID, claims)
}

// CutReasons implements billingapp.ClaimsPort.
func (c *Claims) CutReasons() []string { return c.svc.CutReasonCodes() }

// adjustmentsOf translates the claim module's answer into this package's vocabulary.
func adjustmentsOf(rows []claimapp.AdjustmentRef) []billingapp.ClaimAdjustment {
	out := make([]billingapp.ClaimAdjustment, 0, len(rows))
	for _, row := range rows {
		out = append(out, billingapp.ClaimAdjustment{
			ClaimID: row.ClaimID, AdjustmentID: row.AdjustmentID, Amount: row.Amount,
		})
	}
	return out
}

// RaiseReimbursement implements billingapp.ClaimsPort. It is WP-I7-04's half of the boundary:
// the settlement module decides *when* an approved reimbursement becomes a claim — at the
// approval, never before — and the claim module decides what that claim is.
func (c *Claims) RaiseReimbursement(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, in billingapp.ReimbursementClaim,
) (uuid.UUID, error) {
	return c.svc.RaiseReimbursementClaim(ctx, tx, tenantID, actorID,
		claimapp.ReimbursementClaimInput{
			PersonID: in.PersonID, ProgramID: in.ProgramID, EnrollmentID: in.EnrollmentID,
			ServiceRequestID:    in.ServiceRequestID,
			ServiceDefinitionID: in.ServiceDefinitionID, ServiceDate: in.ServiceDate,
			ApprovedAmount: in.ApprovedAmount, CurrencyCode: in.CurrencyCode,
			ProviderOrganizationID: in.ProviderOrganizationID, ReasonCode: in.ReasonCode,
			DecidedBy: actorID,
		})
}
