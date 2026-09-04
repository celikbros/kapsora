package contracthttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
)

// ListContractVersions implements listContractVersions.
func (h *Handler) ListContractVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	contractID, ok := h.pathUUID(w, r, "contractId", application.ErrContractNotFound)
	if !ok {
		return
	}
	versions, err := h.svc.ListVersions(r.Context(), rc, contractID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ContractVersionList{Items: make([]kapsorav1.ContractVersionSummary, 0, len(versions))}
	for _, v := range versions {
		out.Items = append(out.Items, versionSummaryView(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateContractVersion implements createContractVersion.
func (h *Handler) CreateContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	contractID, ok := h.pathUUID(w, r, "contractId", application.ErrContractNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateContractVersionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewVersionInput{CopyFromVersionID: body.CopyFromVersionId, Notes: body.Notes}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	if body.ValidFrom != nil {
		from := body.ValidFrom.Time
		in.ValidFrom = &from
	}
	if body.ValidTo != nil {
		to := body.ValidTo.Time
		in.ValidTo = &to
	}

	version, err := h.svc.CreateVersion(r.Context(), rc, contractID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(version.Version.RowVersion))
	w.Header().Set("Location", "/api/v1/contract-versions/"+version.Version.ID.String())
	writeJSON(w, http.StatusCreated, versionView(version))
}

// GetContractVersion implements getContractVersion.
func (h *Handler) GetContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
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

// PatchContractVersion implements patchContractVersion (merge-patch with If-Match).
func (h *Handler) PatchContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
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
		case "currencyCode":
			patch.CurrencyCode = decodeString(value, key, &fields)
		case "notes":
			if isJSONNull(value) {
				patch.ClearNotes = true
			} else {
				patch.Notes = decodeString(value, key, &fields)
			}
		case "status", "versionNo", "configurationHash":
			// The status moves through submit, publish and retire, and the hash is
			// written by the publish that froze the sheet.
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

// SubmitContractVersion implements submitContractVersion.
func (h *Handler) SubmitContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
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

// PublishContractVersion implements publishContractVersion; it needs contract.publish, a
// recent step-up and an actor other than the one who submitted the version.
func (h *Handler) PublishContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
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

// RetireContractVersion implements retireContractVersion; it needs contract.publish and a
// recent step-up.
func (h *Handler) RetireContractVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionPublish)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
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

func versionSummaryView(v application.VersionRecord) kapsorav1.ContractVersionSummary {
	out := kapsorav1.ContractVersionSummary{
		Id: v.ID, ContractId: v.ContractID, VersionNo: v.VersionNo,
		Status: kapsorav1.ContractVersionStatus(v.Status), CurrencyCode: v.CurrencyCode,
		Notes: v.Notes, PublishedAt: v.PublishedAt, RowVersion: int(v.RowVersion),
	}
	if v.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *v.ValidFrom}
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	return out
}

func versionView(view application.VersionView) kapsorav1.ContractVersion {
	v := view.Version
	out := kapsorav1.ContractVersion{
		Id: v.ID, ContractId: v.ContractID, VersionNo: v.VersionNo,
		Status: kapsorav1.ContractVersionStatus(v.Status), CurrencyCode: v.CurrencyCode,
		Notes: v.Notes, ConfigurationHash: v.ConfigurationHash,
		SubmittedAt: v.SubmittedAt, SubmittedBy: v.SubmittedBy,
		PublishedAt: v.PublishedAt, PublishedBy: v.PublishedBy,
		ReviewComment: v.ReviewComment, RetireReasonCode: v.RetireReasonCode,
		PriceLists: make([]kapsorav1.PriceList, 0, len(view.PriceLists)),
		RowVersion: int(v.RowVersion),
	}
	if v.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *v.ValidFrom}
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	for _, l := range view.PriceLists {
		out.PriceLists = append(out.PriceLists, priceListView(l))
	}
	return out
}
