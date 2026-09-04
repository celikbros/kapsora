package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// CreateContract opens a contract between a payer organization relationship and a provider
// profile of this tenant. It carries no prices of its own: what was agreed lives in the
// versions, and only a published one is ever read downstream.
func (s *Service) CreateContract(ctx context.Context, rc identity.RequestContext, in domain.NewContract) (ContractRecord, error) {
	if err := domain.ValidateNewContract(in); err != nil {
		return ContractRecord{}, err
	}
	payerID, err := uuid.Parse(in.PayerOrganizationID)
	if err != nil {
		return ContractRecord{}, fieldError("payerOrganizationId", "FORMAT", "geçerli bir kimlik olmalı")
	}
	providerID, err := uuid.Parse(in.ProviderProfileID)
	if err != nil {
		return ContractRecord{}, fieldError("providerProfileId", "FORMAT", "geçerli bir kimlik olmalı")
	}
	sponsorID, err := parseOptionalUUID(in.SponsorOrganizationID, "sponsorOrganizationId")
	if err != nil {
		return ContractRecord{}, err
	}

	var out ContractRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		id, err := s.repo.CreateContract(ctx, tx, rc.TenantID, NewContractRow{
			Code: in.Code, Name: in.Name, PayerOrganizationID: payerID,
			ProviderProfileID: providerID, SponsorOrganizationID: sponsorID,
			DomainCode: in.DomainCode,
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract.create", "contract", id, map[string]any{
			"code": in.Code, "domain_code": in.DomainCode,
			"payer_organization_id": payerID, "provider_profile_id": providerID,
		}); err != nil {
			return err
		}
		out, err = s.repo.GetContract(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// GetContract returns one contract of the tenant.
func (s *Service) GetContract(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (ContractRecord, error) {
	var out ContractRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetContract(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// ListContracts returns one page ordered by creation time, newest first.
func (s *Service) ListContracts(ctx context.Context, rc identity.RequestContext, f ListFilter) (ContractPage, error) {
	ve := &domain.ValidationError{}
	appendFields(ve, domain.ValidateSearchTerm("q", f.Query))
	if f.DomainCode != "" && !domain.Contains(domain.DomainCodes, f.DomainCode) {
		ve.Add("domainCode", "ENUM", "geçersiz hizmet alanı")
	}
	if f.Status != "" && !domain.Contains(domain.ContractStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz sözleşme durumu")
	}
	providerID, err := parseOptionalUUID(f.ProviderProfileID, "providerProfileId")
	if err != nil {
		return ContractPage{}, err
	}
	payerID, err := parseOptionalUUID(f.PayerOrganizationID, "payerOrganizationId")
	if err != nil {
		return ContractPage{}, err
	}
	if err := ve.OrNil(); err != nil {
		return ContractPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ContractPage{}, err
	}

	q := ContractQuery{
		ProviderProfileID: providerID, PayerOrganizationID: payerID,
		DomainCode: f.DomainCode, Status: f.Status, Query: domain.LikePattern(f.Query),
		After: after, PageSize: pageSize + 1,
	}
	var rows []ContractRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListContracts(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return ContractPage{}, err
	}
	page := ContractPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateContract applies a merge-patch under optimistic concurrency. Suspending or closing
// a contract does not touch its versions; it only stops the price selection considering
// them, so the history of what was agreed stays exactly as it was.
func (s *Service) UpdateContract(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.ContractPatch) (ContractRecord, error) {
	if err := domain.ValidateContractPatch(p); err != nil {
		return ContractRecord{}, err
	}
	sponsorID, err := parseOptionalUUID(deref(p.SponsorOrganizationID), "sponsorOrganizationId")
	if err != nil {
		return ContractRecord{}, err
	}

	var out ContractRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetContract(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := ContractUpdateRow{
			Name: current.Name, SponsorOrganizationID: current.SponsorOrganizationID,
			Status: current.Status,
		}
		var fields []string
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			fields = append(fields, "name")
		}
		switch {
		case p.ClearSponsor:
			if current.SponsorOrganizationID != nil {
				fields = append(fields, "sponsorOrganizationId")
			}
			next.SponsorOrganizationID = nil
		case sponsorID != nil:
			if current.SponsorOrganizationID == nil || *current.SponsorOrganizationID != *sponsorID {
				fields = append(fields, "sponsorOrganizationId")
			}
			next.SponsorOrganizationID = sponsorID
		}
		if p.Status != nil && *p.Status != current.Status {
			if err := domain.ValidateTransition(current.Status, *p.Status); err != nil {
				return err
			}
			next.Status = *p.Status
			fields = append(fields, "status")
		}

		if err := s.repo.UpdateContract(ctx, tx, rc.TenantID, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract.update", "contract", id, map[string]any{
			"code": current.Code, "changed_count": len(fields), "changed_fields": changed(fields),
			"to_status": next.Status,
		}); err != nil {
			return err
		}
		out, err = s.repo.GetContract(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}
