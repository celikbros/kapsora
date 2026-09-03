package benefithttp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// The plan version and entitlement definition bodies are hand-written rather than taken
// from the generated types: the contract types map `number` to float32, and an
// entitlement quantity is a numeric(20,6) that must never pass through a float
// (handbook section 3, "Money is numeric(20,6) ... No floats"). json.Number keeps the
// exact decimal text on the wire in both directions; the JSON shape is unchanged.

type entitlementDefinitionInput struct {
	Code            string       `json:"code"`
	Name            string       `json:"name"`
	UnitType        string       `json:"unitType"`
	CurrencyCode    *string      `json:"currencyCode,omitempty"`
	PeriodType      string       `json:"periodType"`
	PeriodLength    *int         `json:"periodLength,omitempty"`
	InitialQuantity json.Number  `json:"initialQuantity"`
	AllowOverdraft  *bool        `json:"allowOverdraft,omitempty"`
	RolloverPolicy  *string      `json:"rolloverPolicy,omitempty"`
	RolloverCap     *json.Number `json:"rolloverCap,omitempty"`
	FamilyShared    *bool        `json:"familyShared,omitempty"`
}

type replaceDefinitionsRequest struct {
	Items []entitlementDefinitionInput `json:"items"`
}

type entitlementDefinitionView struct {
	Id              uuid.UUID    `json:"id"`
	Status          string       `json:"status"`
	Code            string       `json:"code"`
	Name            string       `json:"name"`
	UnitType        string       `json:"unitType"`
	CurrencyCode    *string      `json:"currencyCode,omitempty"`
	PeriodType      string       `json:"periodType"`
	PeriodLength    *int         `json:"periodLength,omitempty"`
	InitialQuantity json.Number  `json:"initialQuantity"`
	AllowOverdraft  bool         `json:"allowOverdraft"`
	RolloverPolicy  string       `json:"rolloverPolicy"`
	RolloverCap     *json.Number `json:"rolloverCap,omitempty"`
	FamilyShared    bool         `json:"familyShared"`
}

// planVersionView is the contract's PlanVersion: the summary fields plus the review
// metadata and the definitions.
type planVersionView struct {
	kapsorav1.PlanVersionSummary
	Notes             *string                     `json:"notes,omitempty"`
	Definitions       []entitlementDefinitionView `json:"definitions"`
	ConfigurationHash *string                     `json:"configurationHash,omitempty"`
	SubmittedBy       *uuid.UUID                  `json:"submittedBy,omitempty"`
	SubmittedAt       *time.Time                  `json:"submittedAt,omitempty"`
	PublishedBy       *uuid.UUID                  `json:"publishedBy,omitempty"`
	ReviewComment     *string                     `json:"reviewComment,omitempty"`
	RetireReasonCode  *string                     `json:"retireReasonCode,omitempty"`
}

// ListPlanVersions implements listPlanVersions.
func (h *Handler) ListPlanVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	planID, ok := h.pathUUID(w, r, "planId", application.ErrPlanNotFound)
	if !ok {
		return
	}
	versions, err := h.svc.ListPlanVersions(r.Context(), rc, planID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ListPlanVersions200JSONResponse{Items: make([]kapsorav1.PlanVersionSummary, 0, len(versions))}
	for _, v := range versions {
		out.Items = append(out.Items, versionSummaryView(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreatePlanVersion implements createPlanVersion.
func (h *Handler) CreatePlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	planID, ok := h.pathUUID(w, r, "planId", application.ErrPlanNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreatePlanVersionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewPlanVersionInput{CopyFromVersionID: body.CopyFromVersionId, Notes: body.Notes}
	if body.ValidFrom != nil {
		from := dateOnly(body.ValidFrom.Time)
		in.ValidFrom = &from
	}
	if body.ValidTo != nil {
		to := dateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	version, err := h.svc.CreatePlanVersion(r.Context(), rc, planID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusCreated, versionView(version))
}

// GetPlanVersion implements getPlanVersion.
func (h *Handler) GetPlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	version, err := h.svc.GetPlanVersion(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// UpdatePlanVersion implements updatePlanVersion (merge-patch with If-Match).
func (h *Handler) UpdatePlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
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

	patch := application.PlanVersionPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "validFrom":
			if isJSONNull(value) {
				patch.ClearValidFrom = true
			} else {
				patch.ValidFrom = decodeDate(value, key, &fields)
			}
		case "validTo":
			if isJSONNull(value) {
				patch.ClearValidTo = true
			} else {
				patch.ValidTo = decodeDate(value, key, &fields)
			}
		case "notes":
			if isJSONNull(value) {
				patch.ClearNotes = true
			} else {
				patch.Notes = decodeString(value, key, &fields)
			}
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	version, err := h.svc.UpdatePlanVersion(r.Context(), rc, versionID, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// ReplaceDefinitions implements replaceEntitlementDefinitions.
func (h *Handler) ReplaceDefinitions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body replaceDefinitionsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	defs := make([]domain.EntitlementDefinition, 0, len(body.Items))
	for _, item := range body.Items {
		d := domain.EntitlementDefinition{
			Code: item.Code, Name: item.Name, UnitType: item.UnitType, PeriodType: item.PeriodType,
			PeriodLength: item.PeriodLength, InitialQuantity: item.InitialQuantity.String(),
		}
		if item.CurrencyCode != nil {
			d.CurrencyCode = *item.CurrencyCode
		}
		if item.AllowOverdraft != nil {
			d.AllowOverdraft = *item.AllowOverdraft
		}
		if item.RolloverPolicy != nil {
			d.RolloverPolicy = *item.RolloverPolicy
		}
		if item.RolloverCap != nil {
			d.RolloverCap = item.RolloverCap.String()
		}
		if item.FamilyShared != nil {
			d.FamilyShared = *item.FamilyShared
		}
		defs = append(defs, d)
	}

	version, err := h.svc.ReplaceDefinitions(r.Context(), rc, versionID, defs, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// SubmitPlanVersion implements submitPlanVersion.
func (h *Handler) SubmitPlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewComment
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	version, err := h.svc.SubmitPlanVersion(r.Context(), rc, versionID, body.Comment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// PublishPlanVersion implements publishPlanVersion; it needs plan.publish and step-up.
func (h *Handler) PublishPlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPlanPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewComment
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	version, err := h.svc.PublishPlanVersion(r.Context(), rc, versionID, body.Comment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// RetirePlanVersion implements retirePlanVersion; it needs plan.publish and step-up.
func (h *Handler) RetirePlanVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPlanPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReasonCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	version, err := h.svc.RetirePlanVersion(r.Context(), rc, versionID, body.ReasonCode, body.ReasonText, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

func versionSummaryView(v application.PlanVersion) kapsorav1.PlanVersionSummary {
	out := kapsorav1.PlanVersionSummary{
		Id: v.ID, PlanId: v.PlanID, VersionNo: v.VersionNo,
		Status: kapsorav1.PlanVersionSummaryStatus(v.Status), RowVersion: int(v.RowVersion),
		PublishedAt: v.PublishedAt,
	}
	if v.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *v.ValidFrom}
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	return out
}

func versionView(v application.PlanVersion) planVersionView {
	out := planVersionView{
		PlanVersionSummary: versionSummaryView(v),
		Notes:              v.Notes,
		Definitions:        make([]entitlementDefinitionView, 0, len(v.Definitions)),
		SubmittedBy:        v.SubmittedBy, SubmittedAt: v.SubmittedAt, PublishedBy: v.PublishedBy,
		ReviewComment: v.ReviewComment, RetireReasonCode: v.RetireReasonCode,
	}
	if v.ConfigurationHash != "" {
		hash := v.ConfigurationHash
		out.ConfigurationHash = &hash
	}
	for _, d := range v.Definitions {
		out.Definitions = append(out.Definitions, definitionView(d))
	}
	return out
}

func definitionView(d application.Definition) entitlementDefinitionView {
	out := entitlementDefinitionView{
		Id: d.ID, Status: d.Status, Code: d.Spec.Code, Name: d.Spec.Name,
		UnitType: d.Spec.UnitType, PeriodType: d.Spec.PeriodType, PeriodLength: d.Spec.PeriodLength,
		InitialQuantity: json.Number(d.Spec.InitialQuantity), AllowOverdraft: d.Spec.AllowOverdraft,
		RolloverPolicy: d.Spec.RolloverPolicy, FamilyShared: d.Spec.FamilyShared,
	}
	if d.Spec.CurrencyCode != "" {
		code := d.Spec.CurrencyCode
		out.CurrencyCode = &code
	}
	if d.Spec.RolloverCap != "" {
		cap := json.Number(d.Spec.RolloverCap)
		out.RolloverCap = &cap
	}
	return out
}
