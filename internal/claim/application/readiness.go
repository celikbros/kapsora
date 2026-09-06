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
	ClaimID          uuid.UUID
	Status           string
	CurrencyCode     string
	ApprovedTotal    string
	PayerTotal       string
	MemberTotal      string
	LineCount        int
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

		totals := sumDecisions(decisions)
		out = InvoiceReadiness{
			ClaimID: record.ID, Status: record.Status, CurrencyCode: currencyOf(lines),
			ApprovedTotal: totals.Approved.String(), PayerTotal: totals.Payer.String(),
			MemberTotal: totals.Member.String(),
			LineCount:   len(lines), DecidedLineCount: len(decisions),
			Blockers: []string{},
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
