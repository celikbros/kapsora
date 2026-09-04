package workflowhttp

import (
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// ListApprovalPolicies implements listApprovalPolicies.
func (h *Handler) ListApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPolicy)
	if !ok {
		return
	}
	rows, err := h.svc.ListPolicies(r.Context(), rc,
		strings.TrimSpace(r.URL.Query().Get("actionCode")),
		strings.TrimSpace(r.URL.Query().Get("scopeCode")))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policyList(rows))
}

// PutApprovalPolicies implements putApprovalPolicies.
func (h *Handler) PutApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPolicy)
	if !ok {
		return
	}
	var body kapsorav1.PutApprovalPolicies
	if !decodeJSON(w, r, &body) {
		return
	}
	set := domain.PolicySet{
		ActionCode: body.ActionCode,
		Policies:   make([]domain.Policy, 0, len(body.Policies)),
	}
	for _, in := range body.Policies {
		policy := domain.Policy{
			ScopeCode: in.ScopeCode,
			// The two amounts stay exact decimal text from the wire to the numeric
			// column: they are never parsed into a float on the way through.
			MinAmount:             deref(in.MinAmount),
			MaxAmount:             deref(in.MaxAmount),
			RequiredApproverCount: 1,
			ValidFrom:             in.ValidFrom.Time,
		}
		if in.RequiredRoleCodes != nil {
			policy.RequiredRoleCodes = *in.RequiredRoleCodes
		}
		if in.RequiredApproverCount != nil {
			policy.RequiredApproverCount = *in.RequiredApproverCount
		}
		if in.ValidTo != nil {
			validTo := in.ValidTo.Time
			policy.ValidTo = &validTo
		}
		set.Policies = append(set.Policies, policy)
	}

	written, err := h.svc.PutPolicies(r.Context(), rc, set)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policyList(written))
}

func policyList(rows []application.PolicyRecord) kapsorav1.ApprovalPolicyList {
	out := kapsorav1.ApprovalPolicyList{Items: make([]kapsorav1.ApprovalPolicy, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, policyView(row))
	}
	return out
}

func policyView(p application.PolicyRecord) kapsorav1.ApprovalPolicy {
	out := kapsorav1.ApprovalPolicy{
		Id: p.ID, ActionCode: p.ActionCode, ScopeCode: p.ScopeCode,
		VersionNo: p.VersionNo, MinAmount: p.MinAmount, MaxAmount: p.MaxAmount,
		RequiredRoleCodes: p.RequiredRoleCodes, RequiredApproverCount: p.RequiredApproverCount,
		ValidFrom:  openapiDate(p.ValidFrom),
		RowVersion: p.RowVersion, CreatedAt: p.CreatedAt.UTC(),
	}
	if p.ValidTo != nil {
		validTo := openapiDate(*p.ValidTo)
		out.ValidTo = &validTo
	}
	return out
}

func openapiDate(t time.Time) openapi_types.Date { return openapi_types.Date{Time: t} }
