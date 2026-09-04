package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// StatusCommand is one of the three explicit moves of the provider status machine. The
// status is never assigned by a patch, so every transition is recorded as an intent with a
// reason rather than as a field that happened to change.
type StatusCommand string

// The commands and the status each one targets.
const (
	CommandActivate  StatusCommand = "activate"
	CommandSuspend   StatusCommand = "suspend"
	CommandTerminate StatusCommand = "terminate"
)

// target reports the status a command moves to.
func (c StatusCommand) target() (string, bool) {
	switch c {
	case CommandActivate:
		return domain.StatusActive, true
	case CommandSuspend:
		return domain.StatusSuspended, true
	case CommandTerminate:
		return domain.StatusTerminated, true
	default:
		return "", false
	}
}

// CreateProvider gives an organization the tenant already knows a provider profile. The
// relationship must carry the PROVIDER role: a profile on a sponsor or a payer would make
// every later "which provider" answer meaningless.
func (s *Service) CreateProvider(ctx context.Context, rc identity.RequestContext, in domain.NewProvider) (ProviderRecord, error) {
	if err := domain.ValidateNewProvider(in); err != nil {
		return ProviderRecord{}, err
	}
	organizationID, err := uuid.Parse(in.TenantOrganizationID)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("tenantOrganizationId", "FORMAT", "geçerli bir kimlik olmalı")
		return ProviderRecord{}, ve
	}

	var out ProviderRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		org, err := s.repo.GetOrganization(ctx, tx, rc.TenantID, organizationID)
		if err != nil {
			if errors.Is(err, ErrOrganizationNotFound) {
				ve := &domain.ValidationError{}
				ve.Add("tenantOrganizationId", "NOT_FOUND", "kurum ilişkisi bulunamadı")
				return ve
			}
			return err
		}
		if org.Role != "PROVIDER" {
			ve := &domain.ValidationError{}
			ve.Add("tenantOrganizationId", "ROLE", "kurum ilişkisi PROVIDER rolünde olmalı")
			return ve
		}

		id, err := s.repo.CreateProvider(ctx, tx, rc.TenantID, NewProviderRow{
			TenantOrganizationID: organizationID, ProviderType: in.ProviderType,
			NetworkTier: optional(in.NetworkTier), ContractedFrom: dayPtr(in.ContractedFrom),
			ContractedTo: dayPtr(in.ContractedTo), Notes: optional(in.Notes),
		})
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.profile.create", "provider_profile", id, map[string]any{
			"tenant_organization_id": organizationID, "provider_type": in.ProviderType,
			"network_tier": in.NetworkTier,
		})); err != nil {
			return err
		}
		out, err = s.repo.GetProvider(ctx, tx, rc.TenantID, scopeOf(rc), id)
		return err
	})
	return out, err
}

// GetProvider returns one provider profile visible to the caller.
func (s *Service) GetProvider(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (ProviderRecord, error) {
	var out ProviderRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetProvider(ctx, tx, rc.TenantID, scopeOf(rc), id)
		return err
	})
	return out, err
}

// ListProviders returns one page ordered by creation time, newest first.
func (s *Service) ListProviders(ctx context.Context, rc identity.RequestContext, f ListFilter) (ProviderPage, error) {
	if err := validateListFilter(f); err != nil {
		return ProviderPage{}, err
	}
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.ProviderStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz sağlayıcı durumu")
	}
	if err := ve.OrNil(); err != nil {
		return ProviderPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ProviderPage{}, err
	}
	q := ProviderQuery{
		ProviderType: f.ProviderType, Status: f.Status, NetworkTier: f.NetworkTier,
		Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
	}

	var rows []ProviderRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListProviders(ctx, tx, rc.TenantID, scopeOf(rc), q)
		return err
	})
	if err != nil {
		return ProviderPage{}, err
	}
	page := ProviderPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateProvider applies a merge-patch under optimistic concurrency. The status is not
// part of the patch: the transport refuses a body carrying it before this is reached.
func (s *Service) UpdateProvider(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.ProviderPatch) (ProviderRecord, error) {
	if err := domain.ValidateProviderPatch(p); err != nil {
		return ProviderRecord{}, err
	}

	var out ProviderRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := ProviderUpdateRow{
			ProviderType: current.ProviderType, NetworkTier: current.NetworkTier,
			ContractedFrom: current.ContractedFrom, ContractedTo: current.ContractedTo,
			Notes: current.Notes,
		}
		var fields []string
		if p.ProviderType != nil && *p.ProviderType != current.ProviderType {
			next.ProviderType = *p.ProviderType
			fields = append(fields, "providerType")
		}
		switch {
		case p.ClearNetworkTier:
			if current.NetworkTier != nil {
				fields = append(fields, "networkTier")
			}
			next.NetworkTier = nil
		case p.NetworkTier != nil:
			value := optional(*p.NetworkTier)
			if !sameString(value, current.NetworkTier) {
				fields = append(fields, "networkTier")
			}
			next.NetworkTier = value
		}
		switch {
		case p.ClearContractedFrom:
			if current.ContractedFrom != nil {
				fields = append(fields, "contractedFrom")
			}
			next.ContractedFrom = nil
		case p.ContractedFrom != nil:
			value := dayPtr(p.ContractedFrom)
			if !sameDate(value, current.ContractedFrom) {
				fields = append(fields, "contractedFrom")
			}
			next.ContractedFrom = value
		}
		switch {
		case p.ClearContractedTo:
			if current.ContractedTo != nil {
				fields = append(fields, "contractedTo")
			}
			next.ContractedTo = nil
		case p.ContractedTo != nil:
			value := dayPtr(p.ContractedTo)
			if !sameDate(value, current.ContractedTo) {
				fields = append(fields, "contractedTo")
			}
			next.ContractedTo = value
		}
		switch {
		case p.ClearNotes:
			if current.Notes != nil {
				fields = append(fields, "notes")
			}
			next.Notes = nil
		case p.Notes != nil:
			value := optional(*p.Notes)
			if !sameString(value, current.Notes) {
				fields = append(fields, "notes")
			}
			next.Notes = value
		}
		// The period is re-checked after the merge, so moving only one end can still be
		// refused when the result would be inverted.
		if err := domain.ValidateContractPeriod(next.ContractedFrom, next.ContractedTo); err != nil {
			return err
		}

		if err := s.repo.UpdateProvider(ctx, tx, rc.TenantID, scope, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.profile.update", "provider_profile", id, map[string]any{
			"changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.repo.GetProvider(ctx, tx, rc.TenantID, scope, id)
		return err
	})
	return out, err
}

// MoveProvider runs one status command. The legality of the move is a domain decision, so
// the same table answers the API, the tests and any future portal.
func (s *Service) MoveProvider(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	cmd StatusCommand, expected int64, reasonCode, reasonText string,
) (ProviderRecord, error) {
	target, ok := cmd.target()
	if !ok {
		return ProviderRecord{}, errors.New("provider: unknown status command")
	}
	if cmd != CommandActivate {
		ve := &domain.ValidationError{}
		if reasonCode == "" {
			ve.Add("reasonCode", "REQUIRED", "zorunlu alan")
		}
		if len(reasonText) > 1000 {
			ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter olmalı")
		}
		if err := ve.OrNil(); err != nil {
			return ProviderRecord{}, err
		}
	}

	var out ProviderRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, id)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if err := domain.ValidateTransition(current.Status, target); err != nil {
			return err
		}
		if err := s.repo.UpdateProviderStatus(ctx, tx, rc.TenantID, scope, id, target, expected); err != nil {
			return err
		}
		event := s.event(rc, "provider.profile."+string(cmd), "provider_profile", id, map[string]any{
			"from_status": current.Status, "to_status": target,
		})
		event.ReasonCode = reasonCode
		if err := s.audit.Record(ctx, tx, event); err != nil {
			return err
		}
		out, err = s.repo.GetProvider(ctx, tx, rc.TenantID, scope, id)
		return err
	})
	return out, err
}
