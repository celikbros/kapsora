package application

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The provider's earnings view: the figure their invoice will be checked against.
//
// It exists because the alternative is a provider adding up their own claim list in a
// spreadsheet and a payer adding up the same list in a different order, and the two
// disagreeing by a kuruş nobody can find. Everything below is summed once, on the server, in
// exact decimals, from the same line decisions and the same adjustment ledger the readiness
// endpoint reads — so the total a provider sees here and the total an invoice is refused for
// are produced by the same arithmetic.
//
// **Per currency, always.** A total across currencies is not a total; it is two numbers
// written next to each other. A claim is denominated once (the domain refuses a mixed line set
// at the door), so every claim falls into exactly one bucket and the buckets are never added
// together.

// EarningsFilter is the getProviderEarnings request.
type EarningsFilter struct {
	// From and To bound the moment the claim was decided. To is exclusive of the day after,
	// so a caller asking for 1–31 March gets everything decided on 31 March.
	From *time.Time
	To   *time.Time
	// CurrencyCode narrows the answer to one bucket. Empty means every bucket.
	CurrencyCode string
}

// EarningsStatusTotal is one lifecycle status inside one currency.
type EarningsStatusTotal struct {
	Status        string
	ClaimCount    int
	ApprovedTotal string
}

// EarningsCurrency is one currency's whole answer.
type EarningsCurrency struct {
	CurrencyCode string
	ClaimCount   int
	// ApprovedTotal is lines minus adjustments across every decided claim in the bucket.
	ApprovedTotal   string
	PayerTotal      string
	MemberTotal     string
	AdjustmentTotal string
	// InvoiceableTotal is the part of ApprovedTotal that is not yet on an invoice. A claim
	// that has reached INVOICED, BATCHED or SETTLED is money already being collected, and
	// counting it again is exactly how a provider comes to be paid twice.
	InvoiceableTotal string
	// InvoiceableClaimIDs is which claims that total is made of, so an invoice can name them
	// rather than a provider guessing which of their claims the figure covers.
	InvoiceableClaimIDs []uuid.UUID
	ByStatus            []EarningsStatusTotal
}

// ProviderEarnings is the whole answer.
type ProviderEarnings struct {
	ProviderOrganizationID uuid.UUID
	ProviderName           string
	From                   *time.Time
	To                     *time.Time
	Currencies             []EarningsCurrency
}

// invoiceableStatuses are the two a claim is invoiceable in. INVOICED, BATCHED and SETTLED are
// deliberately absent: WP-I7-02 moves a claim into the first of them when it links it to an
// invoice, and from that moment the money is on a document somebody is collecting.
func invoiceable(status string) bool {
	return status == domain.StatusApproved || status == domain.StatusPartiallyApproved
}

// ProviderEarnings answers what a provider earned in a period, per currency.
//
// The caller is either the payer, who may ask about any of their providers, or the provider
// themselves, who may ask about exactly one. `checkProviderScope` is the same function the
// create command uses, so "may I act for this provider" has one answer in this package.
func (s *Service) ProviderEarnings(ctx context.Context, rc identity.RequestContext,
	providerID uuid.UUID, f EarningsFilter,
) (ProviderEarnings, error) {
	if f.CurrencyCode != "" && !domain.ValidCurrency(f.CurrencyCode) {
		return ProviderEarnings{}, fieldError("currency", "FORMAT",
			"üç harfli para birimi kodu olmalı")
	}
	if f.From != nil && f.To != nil && f.To.Before(*f.From) {
		return ProviderEarnings{}, fieldError("to", "RANGE",
			"bitiş tarihi başlangıçtan önce olamaz")
	}
	if err := checkProviderScope(rc, providerID); err != nil {
		return ProviderEarnings{}, err
	}

	out := ProviderEarnings{
		ProviderOrganizationID: providerID, From: f.From, To: f.To,
		Currencies: []EarningsCurrency{},
	}
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		name, err := s.repo.ProviderDisplayName(ctx, tx, rc.TenantID, providerID)
		if err != nil {
			return err
		}
		out.ProviderName = name
		rows, err := s.repo.ProviderEarningClaims(ctx, tx, rc.TenantID, EarningsQuery{
			ProviderOrganizationID: providerID, From: f.From, To: exclusiveEnd(f.To),
			CurrencyCode: f.CurrencyCode,
		})
		if err != nil {
			return err
		}
		out.Currencies = groupEarnings(rows)
		return nil
	})
	if err != nil {
		return ProviderEarnings{}, err
	}
	return out, nil
}

// exclusiveEnd turns the caller's inclusive last day into the half-open bound the query uses.
// A period that ended "on the 31st" includes everything decided on the 31st, and a reader who
// had to know that the endpoint meant midnight would find that out from a missing claim.
func exclusiveEnd(to *time.Time) *time.Time {
	if to == nil {
		return nil
	}
	end := domain.DateOnly(*to).AddDate(0, 0, 1)
	return &end
}

// groupEarnings buckets the claims by currency and adds each bucket up, once.
//
// Every figure is a `benefitdomain.Quantity` the whole way. Nothing here becomes a float, and
// nothing is added up anywhere else: this is the total the invoice is checked against, and a
// second implementation of it — in a report, in a frontend — would eventually answer something
// different on the one claim it rounded differently.
func groupEarnings(rows []EarningClaimRecord) []EarningsCurrency {
	type bucket struct {
		out      EarningsCurrency
		approved benefitdomain.Quantity
		payer    benefitdomain.Quantity
		member   benefitdomain.Quantity
		adjusted benefitdomain.Quantity
		billable benefitdomain.Quantity
		byStatus map[string]*struct {
			count int
			total benefitdomain.Quantity
		}
	}
	buckets := map[string]*bucket{}
	order := []string{}
	for _, row := range rows {
		b, ok := buckets[row.CurrencyCode]
		if !ok {
			b = &bucket{
				out: EarningsCurrency{
					CurrencyCode: row.CurrencyCode, InvoiceableClaimIDs: []uuid.UUID{},
					ByStatus: []EarningsStatusTotal{},
				},
				approved: zero(), payer: zero(), member: zero(), adjusted: zero(),
				billable: zero(),
				byStatus: map[string]*struct {
					count int
					total benefitdomain.Quantity
				}{},
			}
			buckets[row.CurrencyCode] = b
			order = append(order, row.CurrencyCode)
		}
		// Lines minus adjustments, per claim, exactly as the readiness endpoint computes it.
		approved := quantityOrZero(row.LineTotal).Sub(quantityOrZero(row.AdjustmentTotal))
		payer := quantityOrZero(row.LinePayerTotal).Sub(quantityOrZero(row.AdjustmentPayerTotal))
		member := quantityOrZero(row.LineMemberTotal).Sub(quantityOrZero(row.AdjustmentMemberTotal))

		b.out.ClaimCount++
		b.approved = b.approved.Add(approved)
		b.payer = b.payer.Add(payer)
		b.member = b.member.Add(member)
		b.adjusted = b.adjusted.Add(quantityOrZero(row.AdjustmentTotal))
		if invoiceable(row.Status) {
			b.billable = b.billable.Add(approved)
			b.out.InvoiceableClaimIDs = append(b.out.InvoiceableClaimIDs, row.ClaimID)
		}
		status, ok := b.byStatus[row.Status]
		if !ok {
			status = &struct {
				count int
				total benefitdomain.Quantity
			}{total: zero()}
			b.byStatus[row.Status] = status
		}
		status.count++
		status.total = status.total.Add(approved)
	}

	out := make([]EarningsCurrency, 0, len(order))
	sort.Strings(order)
	for _, currency := range order {
		b := buckets[currency]
		b.out.ApprovedTotal = b.approved.String()
		b.out.PayerTotal = b.payer.String()
		b.out.MemberTotal = b.member.String()
		b.out.AdjustmentTotal = b.adjusted.String()
		b.out.InvoiceableTotal = b.billable.String()
		statuses := make([]string, 0, len(b.byStatus))
		for status := range b.byStatus {
			statuses = append(statuses, status)
		}
		sort.Strings(statuses)
		for _, status := range statuses {
			row := b.byStatus[status]
			b.out.ByStatus = append(b.out.ByStatus, EarningsStatusTotal{
				Status: status, ClaimCount: row.count, ApprovedTotal: row.total.String(),
			})
		}
		out = append(out, b.out)
	}
	return out
}
