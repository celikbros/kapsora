package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// ListRuleSets returns one page of rule sets, newest first.
func (s *Service) ListRuleSets(ctx context.Context, rc identity.RequestContext, f ListFilter) (RuleSetPage, error) {
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return RuleSetPage{}, err
	}
	if err := domain.ValidateSearchTerm("q", f.Query); err != nil {
		return RuleSetPage{}, err
	}
	if err := validateFilter(f); err != nil {
		return RuleSetPage{}, err
	}

	var rows []RuleSetRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListRuleSets(ctx, tx, rc.TenantID, RuleSetQuery{
			DomainCode: f.DomainCode, Purpose: f.Purpose, Status: f.Status,
			Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
		})
		return err
	})
	if err != nil {
		return RuleSetPage{}, err
	}
	out := RuleSetPage{Items: rows}
	if len(rows) > pageSize {
		out.Items = rows[:pageSize]
		last := out.Items[pageSize-1]
		out.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return out, nil
}

// validateFilter refuses a filter value outside the closed list rather than quietly
// returning nothing, which would read as "there are none" instead of "you asked wrong".
func validateFilter(f ListFilter) error {
	ve := &domain.ValidationError{}
	if f.DomainCode != "" && !contains(domain.DomainCodes, f.DomainCode) {
		ve.Add("domainCode", "ENUM", "geçerli değerler: "+strings.Join(domain.DomainCodes, ", "))
	}
	if f.Purpose != "" && !contains(domain.Purposes, f.Purpose) {
		ve.Add("purpose", "ENUM", "geçerli değerler: "+strings.Join(domain.Purposes, ", "))
	}
	if f.Status != "" && !contains(domain.SetStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçerli değerler: "+strings.Join(domain.SetStatuses, ", "))
	}
	return ve.OrNil()
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// GetRuleSet returns one rule set.
func (s *Service) GetRuleSet(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (RuleSetRecord, error) {
	var out RuleSetRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetRuleSet(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// CreateRuleSet opens a rule set. It decides nothing on its own: a set with no published
// version is a name and a purpose, and the engine has nothing to evaluate until a version
// is written, tested and published.
func (s *Service) CreateRuleSet(ctx context.Context, rc identity.RequestContext, in domain.NewRuleSet) (RuleSetRecord, error) {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	if err := domain.ValidateNewRuleSet(in); err != nil {
		return RuleSetRecord{}, err
	}

	var out RuleSetRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		id, err := s.repo.CreateRuleSet(ctx, tx, rc.TenantID, NewRuleSetRow{
			Code: in.Code, Name: in.Name, DomainCode: in.DomainCode, Purpose: in.Purpose,
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set.create", "rule_set", id, map[string]any{
			"code": in.Code, "domain_code": in.DomainCode, "purpose": in.Purpose,
		}); err != nil {
			return err
		}
		out, err = s.repo.GetRuleSet(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// UpdateRuleSet applies a merge-patch of the name and the status.
func (s *Service) UpdateRuleSet(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	p domain.RuleSetPatch,
) (RuleSetRecord, error) {
	if p.Name != nil {
		trimmed := strings.TrimSpace(*p.Name)
		p.Name = &trimmed
	}
	if err := domain.ValidateRuleSetPatch(p); err != nil {
		return RuleSetRecord{}, err
	}

	var out RuleSetRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetRuleSet(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		next := RuleSetUpdateRow{Name: current.Name, Status: current.Status}
		var changed []string
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			changed = append(changed, "name")
		}
		if p.Status != nil && *p.Status != current.Status {
			next.Status = *p.Status
			changed = append(changed, "status")
		}
		if err := s.repo.UpdateRuleSet(ctx, tx, rc.TenantID, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set.update", "rule_set", id, map[string]any{
			"code": current.Code, "changed": strings.Join(changed, ","),
		}); err != nil {
			return err
		}
		out, err = s.repo.GetRuleSet(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}
