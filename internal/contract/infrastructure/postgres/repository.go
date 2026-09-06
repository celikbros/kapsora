// Package contractpg implements the contract repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every
// statement. Money crosses this boundary as exact decimal text in both directions; no
// float appears anywhere in the path.
package contractpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Repository is stateless; every method takes the caller's transaction.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// PostgreSQL error codes mapped to named application errors.
const (
	uniqueViolation     = "23505"
	exclusionViolation  = "23P01"
	foreignKeyViolation = "23503"
	// integrityConstraint is what the DRAFT-only guard triggers raise (migration 000039).
	// It is not a constraint the schema declares but a rule a trigger states, and the
	// SQLSTATE is the only thing that tells the two apart from here.
	integrityConstraint = "23000"
)

// CreateContract implements application.Repository.
func (Repository) CreateContract(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewContractRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateContract(ctx, sqlcgen.CreateContractParams{
		TenantID: tenantID, Code: in.Code, Name: in.Name,
		PayerOrganizationID: in.PayerOrganizationID, ProviderProfileID: in.ProviderProfileID,
		SponsorOrganizationID: nullUUID(in.SponsorOrganizationID), DomainCode: in.DomainCode,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_contract_code":
				return uuid.Nil, application.ErrContractCodeTaken
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrPartyNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("contract: create contract: %w", err)
	}
	return id, nil
}

// GetContract implements application.Repository.
func (Repository) GetContract(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.ContractRecord, error) {
	row, err := sqlcgen.New(tx).GetContract(ctx, sqlcgen.GetContractParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ContractRecord{}, application.ErrContractNotFound
	}
	if err != nil {
		return application.ContractRecord{}, fmt.Errorf("contract: get contract: %w", err)
	}
	return application.ContractRecord{
		ID: row.ID, Code: row.Code, Name: row.Name,
		PayerOrganizationID: row.PayerOrganizationID, PayerName: row.PayerName,
		ProviderProfileID: row.ProviderProfileID, ProviderName: row.ProviderName,
		SponsorOrganizationID: uuidPtr(row.SponsorOrganizationID), SponsorName: row.SponsorName,
		DomainCode: row.DomainCode, Status: row.Status,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListContracts implements application.Repository.
func (Repository) ListContracts(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.ContractQuery) ([]application.ContractRecord, error) {
	params := sqlcgen.ListContractsParams{
		TenantID: tenantID, ProviderProfileID: nullUUID(q.ProviderProfileID),
		PayerOrganizationID: nullUUID(q.PayerOrganizationID),
		DomainCode:          optionalString(q.DomainCode), Status: optionalString(q.Status),
		Q: optionalString(q.Query), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListContracts(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("contract: list contracts: %w", err)
	}
	out := make([]application.ContractRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ContractRecord{
			ID: r.ID, Code: r.Code, Name: r.Name,
			PayerOrganizationID: r.PayerOrganizationID, PayerName: r.PayerName,
			ProviderProfileID: r.ProviderProfileID, ProviderName: r.ProviderName,
			SponsorOrganizationID: uuidPtr(r.SponsorOrganizationID), SponsorName: r.SponsorName,
			DomainCode: r.DomainCode, Status: r.Status,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateContract implements application.Repository.
func (Repository) UpdateContract(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.ContractUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateContract(ctx, sqlcgen.UpdateContractParams{
		TenantID: tenantID, ID: id, RowVersion: expected, Name: in.Name,
		SponsorOrganizationID: nullUUID(in.SponsorOrganizationID), Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.ErrPartyNotFound
		}
		return fmt.Errorf("contract: update contract: %w", err)
	}
	return nil
}

// NextVersionNo implements application.Repository.
func (Repository) NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, contractID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).NextContractVersionNo(ctx, sqlcgen.NextContractVersionNoParams{
		TenantID: tenantID, ContractID: contractID,
	})
	if err != nil {
		return 0, fmt.Errorf("contract: next version number: %w", err)
	}
	return int(n), nil
}

// CreateVersion implements application.Repository.
func (Repository) CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewVersionRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateContractVersion(ctx, sqlcgen.CreateContractVersionParams{
		TenantID: tenantID, ContractID: in.ContractID, VersionNo: int32(in.VersionNo), //nolint:gosec // version numbers are small positive integers
		ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
		CurrencyCode: in.CurrencyCode, Notes: in.Notes,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return uuid.Nil, application.ErrContractNotFound
		}
		return uuid.Nil, fmt.Errorf("contract: create version: %w", err)
	}
	return id, nil
}

// GetVersion implements application.Repository.
func (Repository) GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetContractVersion(ctx, sqlcgen.GetContractVersionParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("contract: get version: %w", err)
	}
	return versionRecord(row.ID, row.ContractID, row.VersionNo, row.Status, row.ValidFrom, row.ValidTo,
		row.CurrencyCode, row.Notes, row.ConfigurationHash, row.SubmittedAt, row.SubmittedBy,
		row.PublishedAt, row.PublishedBy, row.RetireReasonCode, row.ReviewComment,
		row.CreatedAt, row.RowVersion), nil
}

// ListVersions implements application.Repository.
func (Repository) ListVersions(ctx context.Context, tx pgx.Tx, tenantID, contractID uuid.UUID) ([]application.VersionRecord, error) {
	rows, err := sqlcgen.New(tx).ListContractVersions(ctx, sqlcgen.ListContractVersionsParams{
		TenantID: tenantID, ContractID: contractID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list versions: %w", err)
	}
	out := make([]application.VersionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, versionRecord(r.ID, r.ContractID, r.VersionNo, r.Status, r.ValidFrom, r.ValidTo,
			r.CurrencyCode, r.Notes, r.ConfigurationHash, r.SubmittedAt, r.SubmittedBy,
			r.PublishedAt, r.PublishedBy, r.RetireReasonCode, r.ReviewComment,
			r.CreatedAt, r.RowVersion))
	}
	return out, nil
}

// LockVersion implements application.Repository.
func (Repository) LockVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetContractVersionForUpdate(ctx, sqlcgen.GetContractVersionForUpdateParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("contract: lock version: %w", err)
	}
	return versionRecord(row.ID, row.ContractID, row.VersionNo, row.Status, row.ValidFrom, row.ValidTo,
		row.CurrencyCode, row.Notes, row.ConfigurationHash, row.SubmittedAt, row.SubmittedBy,
		row.PublishedAt, row.PublishedBy, row.RetireReasonCode, row.ReviewComment,
		row.CreatedAt, row.RowVersion), nil
}

// UpdateVersionDraft implements application.Repository.
func (Repository) UpdateVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.VersionDraftRow) error {
	n, err := sqlcgen.New(tx).UpdateContractVersionDraft(ctx, sqlcgen.UpdateContractVersionDraftParams{
		TenantID: tenantID, ID: id, ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
		CurrencyCode: in.CurrencyCode, Notes: in.Notes,
	})
	if err != nil {
		return fmt.Errorf("contract: update draft version: %w", err)
	}
	if n == 0 {
		// The caller already checked the status under a row lock, so nothing matching
		// means the row moved between the lock and the write.
		return application.ErrVersionImmutable
	}
	return nil
}

// TouchVersion implements application.Repository.
func (Repository) TouchVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	if _, err := sqlcgen.New(tx).TouchContractVersion(ctx, sqlcgen.TouchContractVersionParams{
		TenantID: tenantID, ID: id,
	}); err != nil {
		return fmt.Errorf("contract: touch version: %w", err)
	}
	return nil
}

// SubmitVersion implements application.Repository.
func (Repository) SubmitVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.SubmitRow) error {
	n, err := sqlcgen.New(tx).SubmitContractVersion(ctx, sqlcgen.SubmitContractVersionParams{
		TenantID: tenantID, ID: id, ActorID: uuid.NullUUID{UUID: in.ActorID, Valid: in.ActorID != uuid.Nil},
		ReviewComment: in.Comment,
	})
	if err != nil {
		return fmt.Errorf("contract: submit version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// PublishVersion implements application.Repository. The exclusion constraint on published
// overlap is the authority on "two versions covering the same date", so the conflict is
// mapped from the PostgreSQL error rather than pre-checked.
func (Repository) PublishVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.PublishRow) error {
	hash := in.ConfigurationHash
	n, err := sqlcgen.New(tx).PublishContractVersion(ctx, sqlcgen.PublishContractVersionParams{
		TenantID: tenantID, ID: id, ActorID: uuid.NullUUID{UUID: in.ActorID, Valid: in.ActorID != uuid.Nil},
		ConfigurationHash: &hash, ReviewComment: in.Comment,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation {
			return application.ErrVersionOverlap
		}
		return fmt.Errorf("contract: publish version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// RetireVersion implements application.Repository.
func (Repository) RetireVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.RetireRow) error {
	n, err := sqlcgen.New(tx).RetireContractVersion(ctx, sqlcgen.RetireContractVersionParams{
		TenantID: tenantID, ID: id, ReasonCode: &in.ReasonCode, ReasonText: in.ReasonText,
	})
	if err != nil {
		return fmt.Errorf("contract: retire version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// ListPriceLists implements application.Repository.
func (Repository) ListPriceLists(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.PriceListRecord, error) {
	rows, err := sqlcgen.New(tx).ListPriceLists(ctx, sqlcgen.ListPriceListsParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list price lists: %w", err)
	}
	out := make([]application.PriceListRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.PriceListRecord{
			ID: r.ID, ContractVersionID: r.ContractVersionID, Code: r.Code, Name: r.Name,
			Priority: int(r.Priority), SeasonFrom: datePtr(r.SeasonFrom), SeasonTo: datePtr(r.SeasonTo),
			WeekdayMask: maskPtr(r.WeekdayMask), ItemCount: int(r.ItemCount),
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// GetPriceList implements application.Repository.
func (Repository) GetPriceList(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.PriceListRecord, error) {
	r, err := sqlcgen.New(tx).GetPriceList(ctx, sqlcgen.GetPriceListParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PriceListRecord{}, application.ErrPriceListNotFound
	}
	if err != nil {
		return application.PriceListRecord{}, fmt.Errorf("contract: get price list: %w", err)
	}
	return application.PriceListRecord{
		ID: r.ID, ContractVersionID: r.ContractVersionID, Code: r.Code, Name: r.Name,
		Priority: int(r.Priority), SeasonFrom: datePtr(r.SeasonFrom), SeasonTo: datePtr(r.SeasonTo),
		WeekdayMask: maskPtr(r.WeekdayMask), ItemCount: int(r.ItemCount),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// ReplacePriceLists implements application.Repository. The set is written by code, so a
// list that survives keeps its id and the price items under it; then everything the body
// did not name is deleted, taking its items with it by cascade.
func (Repository) ReplacePriceLists(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []application.PriceListRow) error {
	q := sqlcgen.New(tx)
	keep := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		id, err := q.UpsertPriceList(ctx, sqlcgen.UpsertPriceListParams{
			TenantID: tenantID, ContractVersionID: versionID, Code: r.Code, Name: r.Name,
			Priority: priority(r.Priority), SeasonFrom: dateValue(r.SeasonFrom),
			SeasonTo: dateValue(r.SeasonTo), WeekdayMask: mask(r.WeekdayMask),
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return application.ErrPriceListCodeTaken
			}
			return fmt.Errorf("contract: upsert price list: %w", err)
		}
		keep = append(keep, id)
	}
	if _, err := q.DeletePriceListsExcept(ctx, sqlcgen.DeletePriceListsExceptParams{
		TenantID: tenantID, ContractVersionID: versionID, KeepIds: keep,
	}); err != nil {
		return fmt.Errorf("contract: delete price lists: %w", err)
	}
	return nil
}

// TouchPriceList implements application.Repository.
func (Repository) TouchPriceList(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error {
	n, err := sqlcgen.New(tx).TouchPriceList(ctx, sqlcgen.TouchPriceListParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("contract: touch price list: %w", err)
	}
	if n == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// ListPriceItems implements application.Repository.
func (Repository) ListPriceItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.PriceItemQuery) ([]application.PriceItemRecord, error) {
	params := sqlcgen.ListPriceItemsParams{
		TenantID: tenantID, PriceListID: q.PriceListID, PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListPriceItems(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("contract: list price items: %w", err)
	}
	out := make([]application.PriceItemRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.PriceItemRecord{
			ID: r.ID, PriceListID: r.PriceListID,
			ServiceDefinitionID: uuidPtr(r.ServiceDefinitionID), ServiceDefinitionCode: r.ServiceDefinitionCode,
			ServiceCategoryID: uuidPtr(r.ServiceCategoryID), ServiceCategoryCode: r.ServiceCategoryCode,
			PackageDefinitionID: uuidPtr(r.PackageDefinitionID), PackageDefinitionCode: r.PackageDefinitionCode,
			LocationID: uuidPtr(r.LocationID), UnitType: r.UnitType, PricingMethod: r.PricingMethod,
			Amount: decimal(r.Amount), Percent: decimal(r.Percent), FormulaKey: r.FormulaKey,
			MinAmount: decimal(r.MinAmount), MaxAmount: decimal(r.MaxAmount),
			MemberShareMethod: r.MemberShareMethod, MemberShareAmount: decimal(r.MemberShareAmount),
			MemberSharePercent: decimal(r.MemberSharePercent),
			ValidFrom:          dateTime(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			Priority: int(r.Priority), CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// CountPriceItems implements application.Repository.
func (Repository) CountPriceItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountPriceItemsInVersion(ctx, sqlcgen.CountPriceItemsInVersionParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if err != nil {
		return 0, fmt.Errorf("contract: count price items: %w", err)
	}
	return int(n), nil
}

// ReplacePriceItems implements application.Repository.
func (Repository) ReplacePriceItems(ctx context.Context, tx pgx.Tx, tenantID, priceListID uuid.UUID, rows []application.PriceItemRow) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeletePriceItems(ctx, sqlcgen.DeletePriceItemsParams{
		TenantID: tenantID, PriceListID: priceListID,
	}); err != nil {
		return fmt.Errorf("contract: clear price items: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreatePriceItemParams, 0, len(rows))
	for _, r := range rows {
		params = append(params, sqlcgen.CreatePriceItemParams{
			TenantID: tenantID, PriceListID: priceListID,
			ServiceDefinitionID: nullUUID(r.ServiceDefinitionID), ServiceCategoryID: nullUUID(r.ServiceCategoryID),
			PackageDefinitionID: nullUUID(r.PackageDefinitionID), LocationID: nullUUID(r.LocationID),
			UnitType: r.UnitType, PricingMethod: r.PricingMethod,
			Amount: r.Amount, Percent: r.Percent, FormulaKey: r.FormulaKey,
			MinAmount: r.MinAmount, MaxAmount: r.MaxAmount,
			MemberShareMethod: r.MemberShareMethod, MemberShareAmount: r.MemberShareAmount,
			MemberSharePercent: r.MemberSharePercent,
			ValidFrom:          dateValue(&r.ValidFrom), ValidTo: dateValue(r.ValidTo),
			Priority: priority(r.Priority),
		})
	}
	if err := execBatch(q.CreatePriceItem(ctx, params)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.ErrCatalogTargetMissing
		}
		return fmt.Errorf("contract: replace price items: %w", err)
	}
	return nil
}

// ListPackages implements application.Repository; the lines of every package of the
// version are read in one statement rather than one per bundle.
func (Repository) ListPackages(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.PackageRecord, error) {
	q := sqlcgen.New(tx)
	rows, err := q.ListPackageDefinitions(ctx, sqlcgen.ListPackageDefinitionsParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list packages: %w", err)
	}
	lines, err := q.ListPackageLinesInVersion(ctx, sqlcgen.ListPackageLinesInVersionParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list package lines: %w", err)
	}
	byPackage := make(map[uuid.UUID][]application.PackageLineRecord, len(rows))
	for _, l := range lines {
		byPackage[l.PackageDefinitionID] = append(byPackage[l.PackageDefinitionID], application.PackageLineRecord{
			ServiceDefinitionID: l.ServiceDefinitionID, ServiceDefinitionCode: l.ServiceDefinitionCode,
			IncludedQuantity: decimal(l.IncludedQuantity),
		})
	}
	out := make([]application.PackageRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.PackageRecord{
			ID: r.ID, ContractVersionID: r.ContractVersionID, Code: r.Code, Name: r.Name,
			InclusionRule: r.InclusionRule, MinLines: intPtr(r.MinLines), Lines: byPackage[r.ID],
		})
	}
	return out, nil
}

// ReplacePackages implements application.Repository. Packages are matched by code so the
// price items pointing at one survive the write; the lines of a surviving package are
// rewritten, because they are the package rather than something hanging off it.
func (Repository) ReplacePackages(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []application.PackageRow) error {
	q := sqlcgen.New(tx)
	keep := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		id, err := q.UpsertPackageDefinition(ctx, sqlcgen.UpsertPackageDefinitionParams{
			TenantID: tenantID, ContractVersionID: versionID, Code: r.Code, Name: r.Name,
			InclusionRule: r.InclusionRule, MinLines: int32Ptr(r.MinLines),
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return application.ErrPackageCodeTaken
			}
			return fmt.Errorf("contract: upsert package: %w", err)
		}
		keep = append(keep, id)
		if _, err := q.DeletePackageLines(ctx, sqlcgen.DeletePackageLinesParams{
			TenantID: tenantID, PackageDefinitionID: id,
		}); err != nil {
			return fmt.Errorf("contract: clear package lines: %w", err)
		}
		if len(r.Lines) == 0 {
			continue
		}
		params := make([]sqlcgen.CreatePackageLineParams, 0, len(r.Lines))
		for _, line := range r.Lines {
			params = append(params, sqlcgen.CreatePackageLineParams{
				TenantID: tenantID, PackageDefinitionID: id,
				ServiceDefinitionID: line.ServiceDefinitionID, IncludedQuantity: line.IncludedQuantity,
			})
		}
		if err := execBatch(q.CreatePackageLine(ctx, params)); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
				return application.ErrCatalogTargetMissing
			}
			return fmt.Errorf("contract: replace package lines: %w", err)
		}
	}
	if _, err := q.DeletePackageDefinitionsExcept(ctx, sqlcgen.DeletePackageDefinitionsExceptParams{
		TenantID: tenantID, ContractVersionID: versionID, KeepIds: keep,
	}); err != nil {
		return fmt.Errorf("contract: delete packages: %w", err)
	}
	return nil
}

// ListQuotas implements application.Repository.
func (Repository) ListQuotas(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.QuotaRecord, error) {
	rows, err := sqlcgen.New(tx).ListProviderQuotas(ctx, sqlcgen.ListProviderQuotasParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list quotas: %w", err)
	}
	out := make([]application.QuotaRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.QuotaRecord{
			ID: r.ID, ContractVersionID: r.ContractVersionID,
			LocationID: uuidPtr(r.LocationID), ServiceDefinitionID: uuidPtr(r.ServiceDefinitionID),
			PeriodType: r.PeriodType, PeriodFrom: dateTime(r.PeriodFrom), PeriodTo: dateTime(r.PeriodTo),
			Capacity: decimal(r.Capacity), Consumed: decimal(r.Consumed), AllowOverdraft: r.AllowOverdraft,
		})
	}
	return out, nil
}

// ReplaceQuotas implements application.Repository.
func (Repository) ReplaceQuotas(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []application.QuotaRow) error {
	q := sqlcgen.New(tx)
	keep := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		id, err := q.UpsertProviderQuota(ctx, sqlcgen.UpsertProviderQuotaParams{
			TenantID: tenantID, ContractVersionID: versionID,
			LocationID: nullUUID(r.LocationID), ServiceDefinitionID: nullUUID(r.ServiceDefinitionID),
			PeriodType: r.PeriodType, PeriodFrom: dateValue(&r.PeriodFrom), PeriodTo: dateValue(&r.PeriodTo),
			Capacity: r.Capacity, AllowOverdraft: r.AllowOverdraft,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.Code {
				case uniqueViolation:
					return application.ErrQuotaScopeDuplicate
				case foreignKeyViolation:
					return application.ErrCatalogTargetMissing
				}
			}
			return fmt.Errorf("contract: upsert quota: %w", err)
		}
		keep = append(keep, id)
	}
	if _, err := q.DeleteProviderQuotasExcept(ctx, sqlcgen.DeleteProviderQuotasExceptParams{
		TenantID: tenantID, ContractVersionID: versionID, KeepIds: keep,
	}); err != nil {
		return fmt.Errorf("contract: delete quotas: %w", err)
	}
	return nil
}

// GetPaymentTerm implements application.Repository.
func (Repository) GetPaymentTerm(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (application.PaymentTermRecord, error) {
	r, err := sqlcgen.New(tx).GetPaymentTerm(ctx, sqlcgen.GetPaymentTermParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PaymentTermRecord{}, application.ErrPaymentTermNotFound
	}
	if err != nil {
		return application.PaymentTermRecord{}, fmt.Errorf("contract: get payment term: %w", err)
	}
	return application.PaymentTermRecord{
		ID: r.ID, ContractVersionID: r.ContractVersionID, DueDays: int(r.DueDays),
		SettlementMethod: r.SettlementMethod, TaxBehaviour: r.TaxBehaviour,
		VatRate: decimal(r.VatRate), LateFeePercent: decimal(r.LateFeePercent), RowVersion: r.RowVersion,
	}, nil
}

// UpsertPaymentTerm implements application.Repository.
func (Repository) UpsertPaymentTerm(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.PaymentTermRow) error {
	_, err := sqlcgen.New(tx).UpsertPaymentTerm(ctx, sqlcgen.UpsertPaymentTermParams{
		TenantID: tenantID, ContractVersionID: versionID, DueDays: int32(in.DueDays), //nolint:gosec // 0..365, checked by the domain and the column CHECK
		SettlementMethod: in.SettlementMethod, TaxBehaviour: in.TaxBehaviour,
		VatRate: in.VatRate, LateFeePercent: in.LateFeePercent,
	})
	if err != nil {
		return fmt.Errorf("contract: upsert payment term: %w", err)
	}
	return nil
}

// GetLodgingTerms implements application.Repository.
func (Repository) GetLodgingTerms(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (application.LodgingTermsRecord, error) {
	r, err := sqlcgen.New(tx).GetLodgingTerms(ctx, sqlcgen.GetLodgingTermsParams{
		TenantID: tenantID, ContractVersionID: versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LodgingTermsRecord{}, application.ErrLodgingTermsNotFound
	}
	if err != nil {
		return application.LodgingTermsRecord{}, fmt.Errorf("contract: get lodging terms: %w", err)
	}
	return application.LodgingTermsRecord{
		ID: r.ID, ContractVersionID: r.ContractVersionID,
		FreeCancellationHoursBefore: int(r.FreeCancellationHoursBefore),
		PenaltyKind:                 r.PenaltyKind,
		PenaltyNights:               intPtr(r.PenaltyNights),
		PenaltyPercent:              decimal(r.PenaltyPercent),
		NoShowPercent:               decimal(r.NoShowPercent),
		HoldMinutes:                 intPtr(r.HoldMinutes),
		MinNights:                   int(r.MinNights),
		MaxNights:                   intPtr(r.MaxNights),
		ChildFreeUnderAge:           intPtr(r.ChildFreeUnderAge),
		CreatedAt:                   r.CreatedAt, UpdatedAt: r.UpdatedAt, RowVersion: r.RowVersion,
	}, nil
}

// UpsertLodgingTerms implements application.Repository. The DRAFT-only rule is enforced by
// the trigger of migration 000039, which raises integrity_constraint_violation; the
// service checks the status first so the ordinary caller reads a status word rather than a
// database message, and this method surfaces the trigger for the caller that did not.
func (Repository) UpsertLodgingTerms(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.LodgingTermsRow) error {
	_, err := sqlcgen.New(tx).UpsertLodgingTerms(ctx, sqlcgen.UpsertLodgingTermsParams{
		TenantID: tenantID, ContractVersionID: versionID,
		FreeCancellationHoursBefore: int32(in.FreeCancellationHoursBefore), //nolint:gosec // 0..8760, checked by the domain and the column CHECK
		PenaltyKind:                 in.PenaltyKind,
		PenaltyNights:               int32Ptr(in.PenaltyNights),
		PenaltyPercent:              in.PenaltyPercent,
		NoShowPercent:               in.NoShowPercent,
		HoldMinutes:                 int32Ptr(in.HoldMinutes),
		MinNights:                   int32(in.MinNights), //nolint:gosec // 1..365, checked by the domain and the column CHECK
		MaxNights:                   int32Ptr(in.MaxNights),
		ChildFreeUnderAge:           int32Ptr(in.ChildFreeUnderAge),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == integrityConstraint {
			return application.ErrVersionImmutable
		}
		return fmt.Errorf("contract: upsert lodging terms: %w", err)
	}
	return nil
}

// CategoryPath implements application.Repository by walking catalog.service_category
// upward from the definition's own category; the nearest ancestor comes first, which is
// the order the specificity ladder scores.
func (Repository) CategoryPath(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ServiceDefinitionCategoryChain(ctx, sqlcgen.ServiceDefinitionCategoryChainParams{
		TenantID: tenantID, ServiceDefinitionID: definitionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: category path: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// PackagesContaining implements application.Repository.
func (Repository) PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListPackagesContainingDefinition(ctx, sqlcgen.ListPackagesContainingDefinitionParams{
		TenantID: tenantID, ServiceDefinitionID: definitionID,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: packages containing definition: %w", err)
	}
	return rows, nil
}

// ListCandidates implements application.Repository.
func (Repository) ListCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.CandidateQuery) ([]application.CandidateRecord, error) {
	rows, err := sqlcgen.New(tx).ListPriceCandidates(ctx, sqlcgen.ListPriceCandidatesParams{
		TenantID: tenantID, ProviderProfileID: q.ProviderProfileID,
		ServiceDate: dateValue(&q.ServiceDate), ServiceDefinitionID: q.ServiceDefinitionID,
		CategoryIds: nonNil(q.CategoryIDs), PackageIds: nonNil(q.PackageIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("contract: list price candidates: %w", err)
	}
	out := make([]application.CandidateRecord, 0, len(rows))
	for _, r := range rows {
		candidate := selection.Candidate{
			PriceItemID: r.PriceItemID, PriceListID: r.PriceListID, ContractVersionID: r.ContractVersionID,
			LocationID: uuidValue(r.LocationID),
			ValidFrom:  dateTime(r.ValidFrom), ValidTo: dateTime(r.ValidTo),
			SeasonFrom: dateTime(r.SeasonFrom), SeasonTo: dateTime(r.SeasonTo),
			WeekdayMask:  weekdayMask(r.WeekdayMask),
			ItemPriority: int(r.ItemPriority), ListPriority: int(r.ListPriority),
		}
		switch {
		case r.ServiceDefinitionID.Valid:
			candidate.Target = selection.TargetDefinition
			candidate.DefinitionID = r.ServiceDefinitionID.UUID
		case r.PackageDefinitionID.Valid:
			candidate.Target = selection.TargetPackage
			candidate.PackageID = r.PackageDefinitionID.UUID
		case r.ServiceCategoryID.Valid:
			candidate.Target = selection.TargetCategory
			candidate.CategoryID = r.ServiceCategoryID.UUID
		}
		out = append(out, application.CandidateRecord{
			Candidate: candidate,
			Detail: application.PriceDetail{
				ContractID: r.ContractID, ContractCode: r.ContractCode, VersionNo: int(r.VersionNo),
				CurrencyCode: r.CurrencyCode, PriceListCode: r.PriceListCode,
				LocationID: uuidPtr(r.LocationID), UnitType: r.UnitType, PricingMethod: r.PricingMethod,
				Amount: decimal(r.Amount), Percent: decimal(r.Percent), FormulaKey: r.FormulaKey,
				MinAmount: decimal(r.MinAmount), MaxAmount: decimal(r.MaxAmount),
				MemberShareMethod: r.MemberShareMethod, MemberShareAmount: decimal(r.MemberShareAmount),
				MemberSharePercent: decimal(r.MemberSharePercent),
			},
		})
	}
	return out, nil
}

// ServiceDefinitionExists implements application.Repository.
func (Repository) ServiceDefinitionExists(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) (bool, error) {
	present, err := sqlcgen.New(tx).ContractServiceDefinitionExists(ctx, sqlcgen.ContractServiceDefinitionExistsParams{
		TenantID: tenantID, ID: definitionID,
	})
	if err != nil {
		return false, fmt.Errorf("contract: service definition exists: %w", err)
	}
	return present, nil
}

// batchExecutor is the shape the pipelined inserts share.
type batchExecutor interface {
	Exec(func(int, error))
	Close() error
}

// execBatch runs a pipelined insert inside the caller's transaction and returns the first
// error; any failure aborts the whole transaction, so a replacement is all-or-nothing.
func execBatch(batch batchExecutor) error {
	var firstErr error
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// versionRecord maps a generated contract_version row onto the application record; the
// three readers of the table return three distinct generated types with identical fields,
// so the mapping takes them apart rather than being written three times.
func versionRecord(id, contractID uuid.UUID, versionNo int32, status string,
	validFrom, validTo pgtype.Date, currency string, notes, hash *string,
	submittedAt *time.Time, submittedBy uuid.NullUUID, publishedAt *time.Time, publishedBy uuid.NullUUID,
	retireReason, reviewComment *string, createdAt time.Time, rowVersion int64,
) application.VersionRecord {
	return application.VersionRecord{
		ID: id, ContractID: contractID, VersionNo: int(versionNo), Status: status,
		ValidFrom: datePtr(validFrom), ValidTo: datePtr(validTo), CurrencyCode: currency,
		Notes: notes, ConfigurationHash: hash,
		SubmittedAt: submittedAt, SubmittedBy: uuidPtr(submittedBy),
		PublishedAt: publishedAt, PublishedBy: uuidPtr(publishedBy),
		RetireReasonCode: retireReason, ReviewComment: reviewComment,
		CreatedAt: createdAt, RowVersion: rowVersion,
	}
}

// pageSize keeps the int32 conversion in one place; the caller has already clamped it to at
// most httpx.MaxPageSize+1.
func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}

// priority narrows a validated 0..10000 priority to the column's int32.
func priority(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > domain.MaxPriority:
		return domain.MaxPriority
	default:
		return int32(n)
	}
}

// decimal normalises the padded text PostgreSQL renders for a numeric(20,6) back to the
// canonical decimal the contract carries: 300.000000 read from the column is the same
// agreed price as the 300 that was written, and the two must not look different on the
// wire or in the configuration hash.
func decimal(raw string) string { return domain.CanonicalDecimal(raw) }

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func uuidValue(n uuid.NullUUID) uuid.UUID {
	if !n.Valid {
		return uuid.Nil
	}
	return n.UUID
}

func intPtr(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n) //nolint:gosec // validated as a small positive line count
	return &v
}

// mask narrows the validated 1..127 weekday mask to the smallint the column stores.
func mask(n *int) *int16 {
	if n == nil {
		return nil
	}
	v := int16(*n) //nolint:gosec // validated as 1..127 by the domain and the column CHECK
	return &v
}

func maskPtr(n *int16) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

// weekdayMask converts the stored smallint to the uint8 the selection scores; a NULL mask
// is zero, which the selection reads as "every day".
func weekdayMask(n *int16) uint8 {
	if n == nil || *n < 0 || *n > 127 {
		return 0
	}
	return uint8(*n)
}

func dateValue(t *time.Time) pgtype.Date {
	if t == nil || t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: domain.DateOnly(*t), Valid: true}
}

func dateTime(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

// nonNil hands an empty array rather than NULL to `= ANY(...)`, so a request with no
// category chain or no package matches nothing instead of erroring.
func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
