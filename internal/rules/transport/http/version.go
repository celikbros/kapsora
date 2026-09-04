package ruleshttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// ListRuleSetVersions implements listRuleSetVersions.
func (h *Handler) ListRuleSetVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	ruleSetID, ok := h.pathUUID(w, r, "ruleSetId", application.ErrRuleSetNotFound)
	if !ok {
		return
	}
	versions, err := h.svc.ListVersions(r.Context(), rc, ruleSetID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.RuleSetVersionList{Items: make([]kapsorav1.RuleSetVersionSummary, 0, len(versions))}
	for _, v := range versions {
		out.Items = append(out.Items, versionSummaryView(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateRuleSetVersion implements createRuleSetVersion.
func (h *Handler) CreateRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	ruleSetID, ok := h.pathUUID(w, r, "ruleSetId", application.ErrRuleSetNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateRuleSetVersionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewVersionInput{CopyFromVersionID: body.CopyFromVersionId, Notes: body.Notes}
	if body.InputSchema != nil {
		in.InputSchema = schemaOf(*body.InputSchema)
	}
	if body.ValidFrom != nil {
		from := body.ValidFrom.Time
		in.ValidFrom = &from
	}
	if body.ValidTo != nil {
		to := body.ValidTo.Time
		in.ValidTo = &to
	}

	version, err := h.svc.CreateVersion(r.Context(), rc, ruleSetID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	w.Header().Set("Location", "/api/v1/rule-set-versions/"+version.Version.ID.String())
	writeJSON(w, http.StatusCreated, versionView(version))
}

// GetRuleSetVersion implements getRuleSetVersion.
func (h *Handler) GetRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	version, err := h.svc.GetVersion(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// PatchRuleSetVersion implements patchRuleSetVersion (merge-patch with If-Match).
func (h *Handler) PatchRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
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

	patch := application.VersionPatch{ExpectedVersion: expected}
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
		case "inputSchema":
			// An absent schema is different from an empty one: {} withdraws every
			// variable, which is refused when a condition still names one.
			patch.InputSchema = decodeSchema(value, key, &fields)
		case "notes":
			if isJSONNull(value) {
				patch.ClearNotes = true
			} else {
				patch.Notes = decodeString(value, key, &fields)
			}
		case "status", "versionNo", "contentHash", "rules", "testCases":
			// The status moves through submit, publish and retire; the hash is written by
			// the publish that froze the version; the two sets have their own PUT.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	version, err := h.svc.UpdateVersion(r.Context(), rc, versionID, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// SubmitRuleSetVersion implements submitRuleSetVersion; the publish gate lives behind it.
func (h *Handler) SubmitRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
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
	version, err := h.svc.SubmitVersion(r.Context(), rc, versionID, body.Comment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// PublishRuleSetVersion implements publishRuleSetVersion; it needs rule.publish, a recent
// step-up and an actor other than the one who submitted the version.
func (h *Handler) PublishRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
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
	version, err := h.svc.PublishVersion(r.Context(), rc, versionID, body.Comment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// RetireRuleSetVersion implements retireRuleSetVersion; it needs rule.publish and a recent
// step-up.
func (h *Handler) RetireRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
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
	version, err := h.svc.RetireVersion(r.Context(), rc, versionID, body.ReasonCode, body.ReasonText, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	writeJSON(w, http.StatusOK, versionView(version))
}

// schemaOf narrows the generated map of CEL type names to the plain map the domain and the
// evaluator speak.
func schemaOf(in kapsorav1.RuleInputSchema) map[string]string {
	out := make(map[string]string, len(in))
	for name, kind := range in {
		out[name] = string(kind)
	}
	return out
}

func schemaView(in map[string]string) kapsorav1.RuleInputSchema {
	out := make(kapsorav1.RuleInputSchema, len(in))
	for name, kind := range in {
		out[name] = kapsorav1.RuleInputType(kind)
	}
	return out
}

func versionSummaryView(v application.VersionRecord) kapsorav1.RuleSetVersionSummary {
	out := kapsorav1.RuleSetVersionSummary{
		Id: v.ID, RuleSetId: v.RuleSetID, VersionNo: v.VersionNo,
		Status: kapsorav1.RuleSetVersionStatus(v.Status), Notes: v.Notes,
		PublishedAt: v.PublishedAt, RuleCount: v.RuleCount, TestCaseCount: v.TestCaseCount,
		RowVersion: int(v.RowVersion),
	}
	if v.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *v.ValidFrom}
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	return out
}

func versionView(view application.VersionView) kapsorav1.RuleSetVersion {
	v := view.Version
	out := kapsorav1.RuleSetVersion{
		Id: v.ID, RuleSetId: v.RuleSetID, VersionNo: v.VersionNo,
		Status: kapsorav1.RuleSetVersionStatus(v.Status), InputSchema: schemaView(v.InputSchema),
		Notes: v.Notes, ContentHash: v.ContentHash,
		SubmittedAt: v.SubmittedAt, SubmittedBy: v.SubmittedBy,
		PublishedAt: v.PublishedAt, PublishedBy: v.PublishedBy,
		ReviewComment: v.ReviewComment, RetireReasonCode: v.RetireReasonCode,
		Rules:      make([]kapsorav1.Rule, 0, len(view.Rules)),
		TestCases:  make([]kapsorav1.RuleTestCase, 0, len(view.TestCases)),
		RowVersion: int(v.RowVersion),
	}
	if v.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *v.ValidFrom}
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	for _, r := range view.Rules {
		out.Rules = append(out.Rules, ruleView(r))
	}
	for _, c := range view.TestCases {
		out.TestCases = append(out.TestCases, testCaseView(c))
	}
	return out
}
