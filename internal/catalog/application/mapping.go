package application

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// MappingResult is a mapping set together with the ETag of the definition it belongs to.
type MappingResult struct {
	Items      []MappingRecord
	RowVersion int64
}

// ListMappings returns every external code a definition is reported under.
func (s *Service) ListMappings(ctx context.Context, rc identity.RequestContext, definitionID uuid.UUID) (MappingResult, error) {
	var out MappingResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		def, err := s.repo.GetDefinition(ctx, tx, rc.TenantID, definitionID)
		if err != nil {
			return err
		}
		out.RowVersion = def.RowVersion
		out.Items, err = s.repo.ListMappings(ctx, tx, rc.TenantID, definitionID)
		return err
	})
	return out, err
}

// ReplaceMappings swaps the whole mapping set of a definition under the definition's own
// optimistic-concurrency token. The submitted set is checked against itself first, so a
// self-contradicting payload is refused before anything is written; the exclusion
// constraints of migration 000019 remain the authority against the stored rows.
func (s *Service) ReplaceMappings(ctx context.Context, rc identity.RequestContext, definitionID uuid.UUID, items []domain.MappingInput, expected int64) (MappingResult, error) {
	if err := domain.ValidateMappingSet(items); err != nil {
		return MappingResult{}, err
	}
	rows := make([]MappingRow, 0, len(items))
	systems := make(map[uuid.UUID]struct{}, len(items))
	for i, it := range items {
		id, err := uuid.Parse(it.CodeSystemID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add(fieldPath(i, "codeSystemId"), "FORMAT", "geçerli bir kimlik olmalı")
			return MappingResult{}, ve
		}
		systems[id] = struct{}{}
		rows = append(rows, MappingRow{
			CodeSystemID: id, Code: it.Code, ValidFrom: it.ValidFrom, ValidTo: it.ValidTo, Primary: it.Primary,
		})
	}

	var out MappingResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		def, err := s.repo.GetDefinition(ctx, tx, rc.TenantID, definitionID)
		if err != nil {
			return err
		}
		if def.RowVersion != expected {
			return ErrVersionMismatch
		}
		for id := range systems {
			if _, err := s.repo.GetCodeSystem(ctx, tx, rc.TenantID, id); err != nil {
				return codeSystemReference(err, items, id)
			}
		}
		if err := s.repo.ReplaceMappings(ctx, tx, rc.TenantID, definitionID, rows); err != nil {
			return err
		}
		// The mapping rows are children, so the definition is touched explicitly to move
		// the ETag the caller holds.
		if err := s.repo.TouchDefinition(ctx, tx, rc.TenantID, definitionID, expected); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.code_mapping.replace", "service_definition", definitionID, map[string]any{
			"code": def.Code, "mapping_count": len(rows), "code_system_count": len(systems),
		})); err != nil {
			return err
		}
		updated, err := s.repo.GetDefinition(ctx, tx, rc.TenantID, definitionID)
		if err != nil {
			return err
		}
		out.RowVersion = updated.RowVersion
		out.Items, err = s.repo.ListMappings(ctx, tx, rc.TenantID, definitionID)
		return err
	})
	if err != nil {
		return MappingResult{}, err
	}
	return out, nil
}

// codeSystemReference names the first row that pointed at a missing code system, so the
// caller sees which array element to fix instead of a bare 404.
func codeSystemReference(err error, items []domain.MappingInput, missing uuid.UUID) error {
	if !isCodeSystemNotFound(err) {
		return err
	}
	ve := &domain.ValidationError{}
	for i, it := range items {
		if it.CodeSystemID == missing.String() {
			ve.Add(fieldPath(i, "codeSystemId"), "NOT_FOUND", "kod sistemi bulunamadı")
			break
		}
	}
	if ve.Len() == 0 {
		ve.Add("items", "NOT_FOUND", "kod sistemi bulunamadı")
	}
	return ve
}

func fieldPath(index int, field string) string {
	return "items[" + strconv.Itoa(index) + "]." + field
}

func isCodeSystemNotFound(err error) bool {
	return errors.Is(err, ErrCodeSystemNotFound)
}
