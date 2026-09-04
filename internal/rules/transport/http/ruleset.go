package ruleshttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// ListRuleSets implements listRuleSets.
func (h *Handler) ListRuleSets(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListRuleSets(r.Context(), rc, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		DomainCode: q.Get("domainCode"), Purpose: q.Get("purpose"), Status: q.Get("status"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.RuleSetPage{Items: make([]kapsorav1.RuleSet, 0, len(page.Items))}
	for _, s := range page.Items {
		out.Items = append(out.Items, ruleSetView(s))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateRuleSet implements createRuleSet.
func (h *Handler) CreateRuleSet(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	var body kapsorav1.CreateRuleSetRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	record, err := h.svc.CreateRuleSet(r.Context(), rc, domain.NewRuleSet{
		Code: body.Code, Name: body.Name,
		DomainCode: string(body.DomainCode), Purpose: string(body.Purpose),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	w.Header().Set("Location", "/api/v1/rule-sets/"+record.ID.String())
	writeJSON(w, http.StatusCreated, ruleSetView(record))
}

// GetRuleSet implements getRuleSet.
func (h *Handler) GetRuleSet(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "ruleSetId", application.ErrRuleSetNotFound)
	if !ok {
		return
	}
	record, err := h.svc.GetRuleSet(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, ruleSetView(record))
}

// PatchRuleSet implements patchRuleSet (merge-patch with If-Match).
func (h *Handler) PatchRuleSet(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "ruleSetId", application.ErrRuleSetNotFound)
	if !ok {
		return
	}
	if !requireMergePatch(w, r) {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}

	patch := domain.RuleSetPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "code", "domainCode", "purpose":
			// They are what the set is, and every published version was written against
			// them; a rule that changed domain would change what it decided about.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	record, err := h.svc.UpdateRuleSet(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, ruleSetView(record))
}

func ruleSetView(s application.RuleSetRecord) kapsorav1.RuleSet {
	return kapsorav1.RuleSet{
		Id: s.ID, Code: s.Code, Name: s.Name,
		DomainCode:   kapsorav1.ServiceDomain(s.DomainCode),
		Purpose:      kapsorav1.RuleSetPurpose(s.Purpose),
		Status:       kapsorav1.RuleSetStatus(s.Status),
		VersionCount: s.VersionCount, RowVersion: int(s.RowVersion),
	}
}
