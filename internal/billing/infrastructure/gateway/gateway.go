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
