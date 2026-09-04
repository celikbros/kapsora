package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// priceContent assembles everything a publish freezes: the version's period and currency,
// its price lists with their items, its packages, its quotas and its payment term. The
// items name their targets by catalog code rather than by id, so a version copied from
// another and published unchanged hashes the same as its source: the hash proves what was
// agreed, not which rows happened to hold it.
func (s *Service) priceContent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, version VersionRecord) (domain.PriceContent, error) {
	content := domain.PriceContent{
		ValidFrom: version.ValidFrom, ValidTo: version.ValidTo, CurrencyCode: version.CurrencyCode,
	}
	lists, err := s.repo.ListPriceLists(ctx, tx, tenantID, version.ID)
	if err != nil {
		return domain.PriceContent{}, err
	}
	for _, l := range lists {
		items, err := s.allPriceItems(ctx, tx, tenantID, l.ID)
		if err != nil {
			return domain.PriceContent{}, err
		}
		listContent := domain.PriceListContent{
			Code: l.Code, Name: l.Name, Priority: l.Priority,
			SeasonFrom: l.SeasonFrom, SeasonTo: l.SeasonTo, WeekdayMask: l.WeekdayMask,
		}
		for _, it := range items {
			listContent.Items = append(listContent.Items, domain.PriceItemContent{
				DefinitionCode: deref(it.ServiceDefinitionCode),
				CategoryCode:   deref(it.ServiceCategoryCode),
				PackageCode:    deref(it.PackageDefinitionCode),
				LocationID:     uuidText(it.LocationID),
				UnitType:       it.UnitType, PricingMethod: it.PricingMethod,
				Amount: it.Amount, Percent: it.Percent, FormulaKey: deref(it.FormulaKey),
				MinAmount: it.MinAmount, MaxAmount: it.MaxAmount,
				MemberShareMethod: it.MemberShareMethod, MemberShareAmount: it.MemberShareAmount,
				MemberSharePercent: it.MemberSharePercent,
				ValidFrom:          it.ValidFrom, ValidTo: it.ValidTo, Priority: it.Priority,
			})
		}
		content.PriceLists = append(content.PriceLists, listContent)
	}

	packages, err := s.repo.ListPackages(ctx, tx, tenantID, version.ID)
	if err != nil {
		return domain.PriceContent{}, err
	}
	for _, p := range packages {
		pkg := domain.PackageContent{
			Code: p.Code, Name: p.Name, InclusionRule: p.InclusionRule, MinLines: p.MinLines,
		}
		for _, line := range p.Lines {
			pkg.Lines = append(pkg.Lines, domain.PackageLineContent{
				DefinitionCode: deref(line.ServiceDefinitionCode), IncludedQuantity: line.IncludedQuantity,
			})
		}
		content.Packages = append(content.Packages, pkg)
	}

	quotas, err := s.repo.ListQuotas(ctx, tx, tenantID, version.ID)
	if err != nil {
		return domain.PriceContent{}, err
	}
	definitionCodes, err := s.definitionCodes(ctx, tx, tenantID, version.ID)
	if err != nil {
		return domain.PriceContent{}, err
	}
	for _, q := range quotas {
		content.Quotas = append(content.Quotas, domain.QuotaContent{
			LocationID: uuidText(q.LocationID), DefinitionCode: definitionCodes[uuidValue(q.ServiceDefinitionID)],
			PeriodType: q.PeriodType, PeriodFrom: q.PeriodFrom, PeriodTo: q.PeriodTo,
			Capacity: q.Capacity, AllowOverdraft: q.AllowOverdraft,
		})
	}

	term, err := s.repo.GetPaymentTerm(ctx, tx, tenantID, version.ID)
	switch {
	case err == nil:
		content.PaymentTerm = &domain.PaymentTermInput{
			DueDays: term.DueDays, SettlementMethod: term.SettlementMethod,
			TaxBehaviour: term.TaxBehaviour, VatRate: term.VatRate, LateFeePercent: term.LateFeePercent,
		}
	case isPaymentTermMissing(err):
		// A version may be published without one; the hash then records its absence.
	default:
		return domain.PriceContent{}, err
	}
	return content, nil
}

// copyPriceContent copies the whole price sheet of one version onto another. Packages go
// first, because a price item may name one and the copy has to point at the new package
// rather than at the source's. It returns how many price lists were copied.
func (s *Service) copyPriceContent(ctx context.Context, tx pgx.Tx, tenantID, sourceID, targetID uuid.UUID) (int, error) {
	sourcePackages, err := s.repo.ListPackages(ctx, tx, tenantID, sourceID)
	if err != nil {
		return 0, err
	}
	if len(sourcePackages) > 0 {
		rows := make([]PackageRow, 0, len(sourcePackages))
		for _, p := range sourcePackages {
			row := PackageRow{Code: p.Code, Name: p.Name, InclusionRule: p.InclusionRule, MinLines: p.MinLines}
			for _, line := range p.Lines {
				row.Lines = append(row.Lines, PackageLineRow{
					ServiceDefinitionID: line.ServiceDefinitionID, IncludedQuantity: line.IncludedQuantity,
				})
			}
			rows = append(rows, row)
		}
		if err := s.repo.ReplacePackages(ctx, tx, tenantID, targetID, rows); err != nil {
			return 0, err
		}
	}
	// The new packages under the target, keyed by code, so a copied price item that priced
	// a bundle keeps pricing the same bundle rather than the source version's row.
	targetPackages, err := s.repo.ListPackages(ctx, tx, tenantID, targetID)
	if err != nil {
		return 0, err
	}
	packageByCode := make(map[string]uuid.UUID, len(targetPackages))
	for _, p := range targetPackages {
		packageByCode[p.Code] = p.ID
	}
	sourcePackageCode := make(map[uuid.UUID]string, len(sourcePackages))
	for _, p := range sourcePackages {
		sourcePackageCode[p.ID] = p.Code
	}

	lists, err := s.repo.ListPriceLists(ctx, tx, tenantID, sourceID)
	if err != nil {
		return 0, err
	}
	listRows := make([]PriceListRow, 0, len(lists))
	for _, l := range lists {
		listRows = append(listRows, PriceListRow{
			Code: l.Code, Name: l.Name, Priority: l.Priority,
			SeasonFrom: l.SeasonFrom, SeasonTo: l.SeasonTo, WeekdayMask: l.WeekdayMask,
		})
	}
	if len(listRows) > 0 {
		if err := s.repo.ReplacePriceLists(ctx, tx, tenantID, targetID, listRows); err != nil {
			return 0, err
		}
	}
	targetLists, err := s.repo.ListPriceLists(ctx, tx, tenantID, targetID)
	if err != nil {
		return 0, err
	}
	targetListByCode := make(map[string]uuid.UUID, len(targetLists))
	for _, l := range targetLists {
		targetListByCode[l.Code] = l.ID
	}

	for _, l := range lists {
		items, err := s.allPriceItems(ctx, tx, tenantID, l.ID)
		if err != nil {
			return 0, err
		}
		if len(items) == 0 {
			continue
		}
		rows := make([]PriceItemRow, 0, len(items))
		for _, it := range items {
			row := PriceItemRow{
				ServiceDefinitionID: it.ServiceDefinitionID, ServiceCategoryID: it.ServiceCategoryID,
				LocationID: it.LocationID, UnitType: it.UnitType, PricingMethod: it.PricingMethod,
				Amount: optString(it.Amount), Percent: optString(it.Percent), FormulaKey: it.FormulaKey,
				MinAmount: optString(it.MinAmount), MaxAmount: optString(it.MaxAmount),
				MemberShareMethod:  it.MemberShareMethod,
				MemberShareAmount:  optString(it.MemberShareAmount),
				MemberSharePercent: optString(it.MemberSharePercent),
				ValidFrom:          it.ValidFrom, ValidTo: it.ValidTo, Priority: it.Priority,
			}
			if it.PackageDefinitionID != nil {
				code := sourcePackageCode[*it.PackageDefinitionID]
				if id, ok := packageByCode[code]; ok {
					row.PackageDefinitionID = &id
				}
			}
			rows = append(rows, row)
		}
		if err := s.repo.ReplacePriceItems(ctx, tx, tenantID, targetListByCode[l.Code], rows); err != nil {
			return 0, err
		}
	}

	quotas, err := s.repo.ListQuotas(ctx, tx, tenantID, sourceID)
	if err != nil {
		return 0, err
	}
	if len(quotas) > 0 {
		rows := make([]QuotaRow, 0, len(quotas))
		for _, q := range quotas {
			// The consumed counter is deliberately not copied: a new version grants new
			// capacity, and carrying last year's usage into it would be a silent error.
			rows = append(rows, QuotaRow{
				LocationID: q.LocationID, ServiceDefinitionID: q.ServiceDefinitionID,
				PeriodType: q.PeriodType, PeriodFrom: q.PeriodFrom, PeriodTo: q.PeriodTo,
				Capacity: q.Capacity, AllowOverdraft: q.AllowOverdraft,
			})
		}
		if err := s.repo.ReplaceQuotas(ctx, tx, tenantID, targetID, rows); err != nil {
			return 0, err
		}
	}

	term, err := s.repo.GetPaymentTerm(ctx, tx, tenantID, sourceID)
	switch {
	case err == nil:
		if err := s.repo.UpsertPaymentTerm(ctx, tx, tenantID, targetID, PaymentTermRow{
			DueDays: term.DueDays, SettlementMethod: term.SettlementMethod,
			TaxBehaviour: term.TaxBehaviour, VatRate: optString(term.VatRate),
			LateFeePercent: optString(term.LateFeePercent),
		}); err != nil {
			return 0, err
		}
	case isPaymentTermMissing(err):
		// Nothing to copy.
	default:
		return 0, err
	}
	return len(listRows), nil
}

// allPriceItems walks every page of one price list. The hash and the copy both need the
// whole set, and a tariff is read here rather than in the paged API path, so the page size
// stays the API's decision and this one stays the sheet's.
func (s *Service) allPriceItems(ctx context.Context, tx pgx.Tx, tenantID, priceListID uuid.UUID) ([]PriceItemRecord, error) {
	var out []PriceItemRecord
	var after *httpx.Cursor
	for {
		rows, err := s.repo.ListPriceItems(ctx, tx, tenantID, PriceItemQuery{
			PriceListID: priceListID, After: after, PageSize: httpx.MaxPageSize,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < httpx.MaxPageSize {
			return out, nil
		}
		last := rows[len(rows)-1]
		after = &httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

// definitionCodes maps the service definition ids a version's quotas name to their catalog
// codes, so the hash records "which service" rather than "which row".
func (s *Service) definitionCodes(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	packages, err := s.repo.ListPackages(ctx, tx, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	for _, p := range packages {
		for _, line := range p.Lines {
			if line.ServiceDefinitionCode != nil {
				out[line.ServiceDefinitionID] = *line.ServiceDefinitionCode
			}
		}
	}
	lists, err := s.repo.ListPriceLists(ctx, tx, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		items, err := s.allPriceItems(ctx, tx, tenantID, l.ID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.ServiceDefinitionID != nil && it.ServiceDefinitionCode != nil {
				out[*it.ServiceDefinitionID] = *it.ServiceDefinitionCode
			}
		}
	}
	return out, nil
}

// isPaymentTermMissing reports the "this version has none" case, which is not an error for
// the hash or the copy.
func isPaymentTermMissing(err error) bool {
	return errors.Is(err, ErrPaymentTermNotFound)
}

func uuidText(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func uuidValue(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
