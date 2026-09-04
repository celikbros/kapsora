package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// ReplacePriceLists swaps the whole price list set of a DRAFT version under the version's
// own optimistic-concurrency token. Lists are matched by code, so one that survives the
// write keeps its id and the price items hanging off it; one the body does not name is
// deleted with its items.
func (s *Service) ReplacePriceLists(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	items []domain.PriceListInput, expected int64,
) (PriceListResult, error) {
	if err := domain.ValidatePriceLists(items); err != nil {
		return PriceListResult{}, err
	}
	rows := make([]PriceListRow, 0, len(items))
	for _, it := range items {
		priority := it.Priority
		if priority == 0 {
			priority = domain.DefaultPriority
		}
		rows = append(rows, PriceListRow{
			Code: it.Code, Name: it.Name, Priority: priority,
			SeasonFrom: domain.DayPtr(it.SeasonFrom), SeasonTo: domain.DayPtr(it.SeasonTo),
			WeekdayMask: it.WeekdayMask,
		})
	}

	var out PriceListResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.ReplacePriceLists(ctx, tx, rc.TenantID, versionID, rows); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "contract_version.price_lists.replace",
			map[string]any{"price_list_count": len(rows)}); err != nil {
			return err
		}
		out, err = s.priceLists(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// ListPriceItems returns one page of the items of a price list, with the ETag of the list.
func (s *Service) ListPriceItems(ctx context.Context, rc identity.RequestContext, priceListID uuid.UUID,
	cursor string, limit int,
) (PriceItemPage, error) {
	after, pageSize, err := s.paging(cursor, limit)
	if err != nil {
		return PriceItemPage{}, err
	}

	var out PriceItemPage
	var rows []PriceItemRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		list, err := s.repo.GetPriceList(ctx, tx, rc.TenantID, priceListID)
		if err != nil {
			return err
		}
		out.ListID, out.RowVersion = list.ID, list.RowVersion
		rows, err = s.repo.ListPriceItems(ctx, tx, rc.TenantID, PriceItemQuery{
			PriceListID: priceListID, After: after, PageSize: pageSize + 1,
		})
		return err
	})
	if err != nil {
		return PriceItemPage{}, err
	}
	out.Items = rows
	if len(rows) > pageSize {
		out.Items = rows[:pageSize]
		last := out.Items[pageSize-1]
		out.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return out, nil
}

// ReplacePriceItems swaps the whole item set of one price list. If-Match carries the ETag
// of the list; the owning version has to be a draft, and it is touched as well so the
// version's own ETag moves with its content.
func (s *Service) ReplacePriceItems(ctx context.Context, rc identity.RequestContext, priceListID uuid.UUID,
	items []domain.PriceItemInput, expected int64,
) (PriceItemPage, error) {
	if err := domain.ValidatePriceItems(items); err != nil {
		return PriceItemPage{}, err
	}
	rows, err := priceItemRows(items)
	if err != nil {
		return PriceItemPage{}, err
	}

	var out PriceItemPage
	var page []PriceItemRecord
	pageSize := httpxDefaultPage
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		list, err := s.repo.GetPriceList(ctx, tx, rc.TenantID, priceListID)
		if err != nil {
			return err
		}
		if list.RowVersion != expected {
			return ErrVersionMismatch
		}
		version, err := s.repo.LockVersion(ctx, tx, rc.TenantID, list.ContractVersionID)
		if err != nil {
			return err
		}
		if version.Status != domain.VersionDraft {
			return statusError(version.Status)
		}
		if err := s.repo.ReplacePriceItems(ctx, tx, rc.TenantID, priceListID, rows); err != nil {
			return err
		}
		// The items are child rows of the list and grandchildren of the version, so both
		// ETags move: a caller holding either one learns the sheet changed.
		if err := s.repo.TouchPriceList(ctx, tx, rc.TenantID, priceListID, expected); err != nil {
			return err
		}
		if err := s.repo.TouchVersion(ctx, tx, rc.TenantID, version.ID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract_version.price_items.replace", "price_list", priceListID, map[string]any{
			"contract_version_id": version.ID, "price_list_code": list.Code, "price_item_count": len(rows),
		}); err != nil {
			return err
		}
		updated, err := s.repo.GetPriceList(ctx, tx, rc.TenantID, priceListID)
		if err != nil {
			return err
		}
		out.ListID, out.RowVersion = updated.ID, updated.RowVersion
		page, err = s.repo.ListPriceItems(ctx, tx, rc.TenantID, PriceItemQuery{
			PriceListID: priceListID, PageSize: pageSize + 1,
		})
		return err
	})
	if err != nil {
		return PriceItemPage{}, err
	}
	out.Items = page
	if len(page) > pageSize {
		out.Items = page[:pageSize]
		last := out.Items[pageSize-1]
		out.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return out, nil
}

// ListPackages returns the packages of a version with the ETag of the version.
func (s *Service) ListPackages(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (PackageResult, error) {
	var out PackageResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Items, err = s.repo.ListPackages(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// ReplacePackages swaps the whole package set of a DRAFT version, lines included.
func (s *Service) ReplacePackages(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	items []domain.PackageInput, expected int64,
) (PackageResult, error) {
	if err := domain.ValidatePackages(items); err != nil {
		return PackageResult{}, err
	}
	rows := make([]PackageRow, 0, len(items))
	for i, pkg := range items {
		rule := pkg.InclusionRule
		if rule == "" {
			rule = domain.InclusionAll
		}
		row := PackageRow{Code: pkg.Code, Name: pkg.Name, InclusionRule: rule, MinLines: pkg.MinLines}
		for j, line := range pkg.Lines {
			id, err := uuid.Parse(line.ServiceDefinitionID)
			if err != nil {
				return PackageResult{}, fieldError(
					itemPath(i)+".lines["+itoa(j)+"].serviceDefinitionId", "FORMAT", "geçerli bir kimlik olmalı")
			}
			row.Lines = append(row.Lines, PackageLineRow{
				ServiceDefinitionID: id, IncludedQuantity: domain.CanonicalDecimal(line.IncludedQuantity),
			})
		}
		rows = append(rows, row)
	}

	var out PackageResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.ReplacePackages(ctx, tx, rc.TenantID, versionID, rows); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "contract_version.packages.replace",
			map[string]any{"package_count": len(rows)}); err != nil {
			return err
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Items, err = s.repo.ListPackages(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// ListQuotas returns the provider quotas of a version with the ETag of the version.
func (s *Service) ListQuotas(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (QuotaResult, error) {
	var out QuotaResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Items, err = s.repo.ListQuotas(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// ReplaceQuotas swaps the whole quota set of a DRAFT version. A quota already present
// under the same scope and period keeps its consumed counter, which authorization owns.
func (s *Service) ReplaceQuotas(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	items []domain.QuotaInput, expected int64,
) (QuotaResult, error) {
	if err := domain.ValidateQuotas(items); err != nil {
		return QuotaResult{}, err
	}
	rows := make([]QuotaRow, 0, len(items))
	for i, it := range items {
		locationID, err := parseOptionalUUID(it.LocationID, itemPath(i)+".locationId")
		if err != nil {
			return QuotaResult{}, err
		}
		definitionID, err := parseOptionalUUID(it.ServiceDefinitionID, itemPath(i)+".serviceDefinitionId")
		if err != nil {
			return QuotaResult{}, err
		}
		rows = append(rows, QuotaRow{
			LocationID: locationID, ServiceDefinitionID: definitionID, PeriodType: it.PeriodType,
			PeriodFrom: domain.DateOnly(it.PeriodFrom), PeriodTo: domain.DateOnly(it.PeriodTo),
			Capacity: domain.CanonicalDecimal(it.Capacity), AllowOverdraft: it.AllowOverdraft,
		})
	}

	var out QuotaResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.ReplaceQuotas(ctx, tx, rc.TenantID, versionID, rows); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "contract_version.quotas.replace",
			map[string]any{"quota_count": len(rows)}); err != nil {
			return err
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Items, err = s.repo.ListQuotas(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// GetPaymentTerm returns the single payment term of a version.
func (s *Service) GetPaymentTerm(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (PaymentTermResult, error) {
	var out PaymentTermResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Term, err = s.repo.GetPaymentTerm(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// PutPaymentTerm writes the single payment term of a DRAFT version, creating or replacing.
func (s *Service) PutPaymentTerm(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	in domain.PaymentTermInput, expected int64,
) (PaymentTermResult, error) {
	if err := domain.ValidatePaymentTerm(in); err != nil {
		return PaymentTermResult{}, err
	}
	row := PaymentTermRow{
		DueDays: in.DueDays, SettlementMethod: in.SettlementMethod, TaxBehaviour: in.TaxBehaviour,
		VatRate:        optString(domain.CanonicalDecimal(in.VatRate)),
		LateFeePercent: optString(domain.CanonicalDecimal(in.LateFeePercent)),
	}

	var out PaymentTermResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.UpsertPaymentTerm(ctx, tx, rc.TenantID, versionID, row); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "contract_version.payment_term.write",
			map[string]any{"settlement_method": in.SettlementMethod, "tax_behaviour": in.TaxBehaviour}); err != nil {
			return err
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Term, err = s.repo.GetPaymentTerm(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// touchAndAudit moves the version's ETag after a child set write and records the change.
func (s *Service) touchAndAudit(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	version VersionRecord, action string, detail map[string]any,
) error {
	if err := s.repo.TouchVersion(ctx, tx, rc.TenantID, version.ID); err != nil {
		return err
	}
	detail["contract_id"] = version.ContractID
	detail["version_no"] = version.VersionNo
	return s.record(ctx, tx, rc, action, "contract_version", version.ID, detail)
}

// priceLists reads the price list set together with the version ETag it is written under.
func (s *Service) priceLists(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (PriceListResult, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return PriceListResult{}, err
	}
	items, err := s.repo.ListPriceLists(ctx, tx, tenantID, versionID)
	if err != nil {
		return PriceListResult{}, err
	}
	return PriceListResult{Items: items, RowVersion: version.RowVersion}, nil
}

// priceItemRows converts a validated request set into repository rows. Every decimal is
// normalised to its canonical form, so the same agreed price always stores the same text
// and therefore hashes the same at publish.
func priceItemRows(items []domain.PriceItemInput) ([]PriceItemRow, error) {
	rows := make([]PriceItemRow, 0, len(items))
	for i, it := range items {
		definitionID, err := parseOptionalUUID(it.ServiceDefinitionID, itemPath(i)+".serviceDefinitionId")
		if err != nil {
			return nil, err
		}
		categoryID, err := parseOptionalUUID(it.ServiceCategoryID, itemPath(i)+".serviceCategoryId")
		if err != nil {
			return nil, err
		}
		packageID, err := parseOptionalUUID(it.PackageDefinitionID, itemPath(i)+".packageDefinitionId")
		if err != nil {
			return nil, err
		}
		locationID, err := parseOptionalUUID(it.LocationID, itemPath(i)+".locationId")
		if err != nil {
			return nil, err
		}
		share := it.MemberShareMethod
		if share == "" {
			share = domain.ShareNone
		}
		priority := it.Priority
		if priority == 0 {
			priority = domain.DefaultPriority
		}
		rows = append(rows, PriceItemRow{
			ServiceDefinitionID: definitionID, ServiceCategoryID: categoryID,
			PackageDefinitionID: packageID, LocationID: locationID,
			UnitType: it.UnitType, PricingMethod: it.PricingMethod,
			Amount:             optString(domain.CanonicalDecimal(it.Amount)),
			Percent:            optString(domain.CanonicalDecimal(it.Percent)),
			FormulaKey:         optString(it.FormulaKey),
			MinAmount:          optString(domain.CanonicalDecimal(it.MinAmount)),
			MaxAmount:          optString(domain.CanonicalDecimal(it.MaxAmount)),
			MemberShareMethod:  share,
			MemberShareAmount:  optString(domain.CanonicalDecimal(it.MemberShareAmount)),
			MemberSharePercent: optString(domain.CanonicalDecimal(it.MemberSharePercent)),
			ValidFrom:          domain.DateOnly(it.ValidFrom),
			ValidTo:            domain.DayPtr(it.ValidTo),
			Priority:           priority,
		})
	}
	return rows, nil
}
