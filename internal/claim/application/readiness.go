package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The named blockers. They are named rather than counted so a screen can say what to fix
// rather than "not ready", and so M7 can act on each of them differently: a missing tax
// identity is a directory correction, a mixed currency is a new version, and an undecided line
// is a reviewer's job.
const (
	// BlockerProviderTaxIdentity is a provider organization with no tax identity to invoice
	// against. It is answered as a boolean from a blind index; the number itself never
	// leaves the encrypted column.
	BlockerProviderTaxIdentity = "PROVIDER_TAX_IDENTITY_MISSING"
	// BlockerCurrencyNotSingle is a claim whose lines are not all in one currency. An
	// invoice is denominated once.
	BlockerCurrencyNotSingle = "CURRENCY_NOT_SINGLE"
	// BlockerLineNotDecided is a line of the current version that still carries no decision.
	BlockerLineNotDecided = "LINE_NOT_DECIDED"
)

// InvoiceReadiness is what M7's invoice will need, answered now.
//
// Every total is summed here, on the server, once, from the line decisions — never from the
// claim lines, which are what the provider asked for rather than what the payer answered, and
// never in a frontend, which would be a second sum that could disagree with this one.
type InvoiceReadiness struct {
	ClaimID      uuid.UUID
	Status       string
	CurrencyCode string
	// LineTotal is what the line decisions approved, before any adjustment.
	LineTotal string
	// AdjustmentTotal is the signed sum of the claim's adjustments: a cut and a recovery add
	// to it, a correction may go either way, and a reversal subtracts exactly what the row
	// it reverses added. It is the second half of the sentence below.
	AdjustmentTotal string
	// ApprovedTotal is lines minus adjustments. It is the figure an invoice is checked
	// against, and it is computed here, once, in exact decimals — never in a frontend, and
	// never twice from two tables that could disagree.
	ApprovedTotal    string
	PayerTotal       string
	MemberTotal      string
	LineCount        int
	AdjustmentCount  int
	DecidedLineCount int
	Ready            bool
	Blockers         []string
}

// InvoiceReadiness answers, for an APPROVED or PARTIALLY_APPROVED claim, what an invoice would
// need. Anything else is refused rather than answered `ready: false`: "is this invoiceable" is
// not a question about a draft, and a false would read as "not yet" when the truthful answer
// is "nobody has decided it".
func (s *Service) InvoiceReadiness(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (InvoiceReadiness, error) {
	var out InvoiceReadiness
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Decided(record.Status) {
			return ErrNotDecided
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, record.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		decisions, err := s.repo.ListLatestDecisions(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		hasTaxIdentity, err := s.repo.ProviderHasTaxIdentity(ctx, tx, rc.TenantID,
			record.ProviderOrganizationID)
		if err != nil {
			return err
		}

		adjustments, err := s.repo.ListAdjustments(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}

		lineTotals := sumDecisions(decisions)
		adjusted := sumAdjustments(adjustments)
		net := lineTotals.Sub(adjusted)
		out = InvoiceReadiness{
			ClaimID: record.ID, Status: record.Status, CurrencyCode: currencyOf(lines),
			LineTotal: lineTotals.Approved.String(), AdjustmentTotal: adjusted.Approved.String(),
			ApprovedTotal: net.Approved.String(), PayerTotal: net.Payer.String(),
			MemberTotal: net.Member.String(),
			LineCount:   len(lines), AdjustmentCount: len(adjustments),
			DecidedLineCount: len(decisions),
			Blockers:         []string{},
		}
		if !hasTaxIdentity {
			out.Blockers = append(out.Blockers, BlockerProviderTaxIdentity)
		}
		if !singleCurrency(lines) {
			out.Blockers = append(out.Blockers, BlockerCurrencyNotSingle)
		}
		if len(decisions) != len(lines) {
			out.Blockers = append(out.Blockers, BlockerLineNotDecided)
		}
		out.Ready = len(out.Blockers) == 0
		return nil
	})
	if err != nil {
		return InvoiceReadiness{}, err
	}
	return out, nil
}

// singleCurrency reports whether every line is denominated the same way. The domain refuses a
// mixed set on the way in, so this is the belt to that braces: a claim whose lines were
// written before the rule existed, or by a path that did not go through the command, still
// answers honestly rather than producing an invoice in two currencies.
func singleCurrency(lines []LineRecord) bool {
	currency := ""
	for _, line := range lines {
		if currency == "" {
			currency = line.CurrencyCode
			continue
		}
		if line.CurrencyCode != currency {
			return false
		}
	}
	return true
}

// sumAdjustments totals the adjustment ledger, signed. A reversal carries the negative of
// what it reverses, so a cut of a hundred followed by its reversal sums to nothing and the
// approved total goes back to exactly where it was — not to within a kuruş of it, exactly,
// because every figure here is an exact decimal that never became a float.
func sumAdjustments(rows []AdjustmentRecord) pipelineTotals {
	out := pipelineTotals{Approved: zero(), Payer: zero(), Member: zero()}
	for _, row := range rows {
		out.Approved = out.Approved.Add(quantityOrZero(row.Amount))
		out.Payer = out.Payer.Add(quantityOrZero(row.PayerAmount))
		out.Member = out.Member.Add(quantityOrZero(row.MemberAmount))
	}
	return out
}

// Sub is "lines minus adjustments", on all three figures at once. The three are subtracted
// together rather than one at a time because the split has to survive the arithmetic: an
// adjustment whose halves add up to its amount leaves a net whose halves add up to the net.
func (p pipelineTotals) Sub(other pipelineTotals) pipelineTotals {
	return pipelineTotals{
		Approved: p.Approved.Sub(other.Approved),
		Payer:    p.Payer.Sub(other.Payer),
		Member:   p.Member.Sub(other.Member),
	}
}
