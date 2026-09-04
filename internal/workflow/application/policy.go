package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// ListPolicies reads the approval policies of the tenant, optionally for one action.
func (s *Service) ListPolicies(ctx context.Context, rc identity.RequestContext,
	actionCode, scopeCode string,
) ([]PolicyRecord, error) {
	var rows []PolicyRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListPolicies(ctx, tx, rc.TenantID, actionCode, scopeCode)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// PutPolicies replaces the whole policy set of one action, in one transaction.
//
// It is a replace rather than a merge because a policy set is read as a whole: "which
// roles may approve six thousand lira" is answered by the set that is there, and a merge
// would leave behind a band the caller believed it had removed. The version number carries
// on from what the action already had, so the numbers say how many times the answer has
// been changed rather than restarting at one after every edit.
func (s *Service) PutPolicies(ctx context.Context, rc identity.RequestContext,
	set domain.PolicySet,
) ([]PolicyRecord, error) {
	if err := set.Validate(); err != nil {
		return nil, err
	}
	var written []PolicyRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.NextPolicyVersion(ctx, tx, rc.TenantID, set.ActionCode)
		if err != nil {
			return err
		}
		removed, err := s.repo.DeletePolicies(ctx, tx, rc.TenantID, set.ActionCode)
		if err != nil {
			return err
		}
		written = make([]PolicyRecord, 0, len(set.Policies))
		for _, policy := range set.Policies {
			record, err := s.repo.CreatePolicy(ctx, tx, rc.TenantID, NewPolicyRow{
				ActionCode: set.ActionCode, ScopeCode: policy.ScopeCode, VersionNo: version,
				MinAmount: amountPtr(policy.MinAmount), MaxAmount: amountPtr(policy.MaxAmount),
				RequiredRoleCodes:     roleCodes(policy.RequiredRoleCodes),
				RequiredApproverCount: policy.RequiredApproverCount,
				ValidFrom:             domain.DateOnly(policy.ValidFrom), ValidTo: dateOnlyPtr(policy.ValidTo),
				ActorID: actorPtr(rc.Principal.ActorID),
			})
			if err != nil {
				return err
			}
			written = append(written, record)
		}
		// The set has no id of its own — it is every row for one action — so the audit
		// row names the action in its detail and carries no resource id.
		return s.record(ctx, tx, rc, "approval_policy.put", "APPROVAL_POLICY", uuid.Nil,
			map[string]any{
				"action_code": set.ActionCode, "version_no": version,
				"policy_count": len(written), "removed_count": removed,
			})
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// ResolvePolicy answers the two questions a command asks before it approves something:
// which roles may approve, and how many approvals this amount needs.
//
// The amount is exact decimal text the whole way. A band compared as a float would refuse
// an approval it should have allowed, one hundredth of a lira from its edge, and nobody
// would ever find out why.
func (s *Service) ResolvePolicy(ctx context.Context, rc identity.RequestContext,
	lookup PolicyLookup,
) (PolicyRecord, error) {
	if _, err := benefitdomain.ParseQuantity(lookup.Amount); err != nil {
		return PolicyRecord{}, fieldError("amount", "FORMAT", "kesin ondalık bir sayı olmalı")
	}
	if lookup.AsOf.IsZero() {
		lookup.AsOf = s.now()
	}
	lookup.AsOf = domain.DateOnly(lookup.AsOf)
	var record PolicyRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		record, err = s.repo.ResolvePolicy(ctx, tx, rc.TenantID, lookup)
		return err
	})
	if err != nil {
		return PolicyRecord{}, err
	}
	return record, nil
}

// amountPtr keeps an open band edge open: an empty string is "no limit", not zero.
func amountPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	value := raw
	return &value
}

// roleCodes keeps an empty list empty rather than null: "no role is required" is a
// decision, and a null column would say the policy had not made one.
func roleCodes(codes []string) []string {
	if codes == nil {
		return []string{}
	}
	return codes
}

func dateOnlyPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := domain.DateOnly(*t)
	return &value
}
