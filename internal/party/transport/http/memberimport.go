package partyhttp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// PermissionImport guards every member import operation (migration 000008).
const PermissionImport = "import.execute"

// maxUploadBytes is the multipart limit of the member file (WP-I2-05 section 2.2). The
// parser refuses anything larger anyway; this stops the transfer earlier.
const maxUploadBytes = 20 << 20

// multipartMemoryBytes is how much of the upload is buffered in memory before Go spills
// the rest to a temporary file.
const multipartMemoryBytes = 8 << 20

// ImportHandler serves /api/v1/imports/members. The uploaded bytes never leave the
// request: the service hashes them, stages the rows and discards the file.
type ImportHandler struct {
	svc    *memberimport.Service
	deny   Denier
	logger *slog.Logger
}

// NewImportHandler wires the handler.
func NewImportHandler(svc *memberimport.Service, deny Denier, logger *slog.Logger) *ImportHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ImportHandler{svc: svc, deny: deny, logger: logger}
}

// Routes mounts everything below /imports/members. `create` is the idempotency middleware
// of the upload command; the review, apply and cancel commands are guarded by If-Match
// instead, and apply is idempotent by batch status.
func (h *ImportHandler) Routes(r chi.Router, create func(http.Handler) http.Handler) {
	r.Get("/", h.List)
	r.With(h.requireUploadStepUp, wrap(create)).Post("/", h.Upload)
	r.Get("/{importId}", h.Get)
	r.Get("/{importId}/rows", h.ListRows)
	r.Post("/{importId}/rows/{rowId}/review", h.Review)
	r.Post("/{importId}/apply", h.Apply)
	r.Post("/{importId}/cancel", h.Cancel)
}

// requireUploadStepUp runs before idempotency so challenges are never cached and
// replaying a stored response still requires current import permission and step-up.
func (h *ImportHandler) requireUploadStepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := identity.RequireStepUp(r.Context(), PermissionImport); err != nil {
			h.deny.Deny(w, r, err, PermissionImport)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Upload implements createMemberImport: multipart file plus the sponsor and source
// identity of the batch. Requires a recent step-up because a member file is bulk personal
// data entering the tenant.
func (h *ImportHandler) Upload(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), PermissionImport)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionImport)
		return
	}
	// The body is bounded before parsing, so the form cannot exhaust memory: at most
	// multipartMemoryBytes stays in RAM and the remainder spills to a temporary file that
	// RemoveAll deletes below (gosec G120 is satisfied by the reader, not by the limit
	// argument, which only sets the memory/disk split).
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+multipartMemoryBytes)
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil { //nolint:gosec // G120: body already bounded by MaxBytesReader above
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, r, http.StatusRequestEntityTooLarge, "import/file-too-large", "IMPORT_FILE_TOO_LARGE",
				"Dosya çok büyük", fmt.Sprintf("En fazla %d MB yükleyebilirsiniz.", maxUploadBytes>>20))
			return
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "multipart/form-data bekleniyor.")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	in, ok := h.uploadInput(w, r)
	if !ok {
		return
	}

	batch, queued, err := h.svc.Upload(r.Context(), rc, in)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	status := http.StatusCreated
	if queued {
		status = http.StatusAccepted
	}
	w.Header().Set("ETag", etag(batch.RowVersion))
	w.Header().Set("Location", "/api/v1/imports/members/"+batch.ID.String())
	writeJSON(w, status, importBatchView(batch))
}

// uploadInput reads and checks the multipart fields.
func (h *ImportHandler) uploadInput(w http.ResponseWriter, r *http.Request) (memberimport.UploadInput, bool) {
	var in memberimport.UploadInput
	file, header, err := r.FormFile("file")
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "`file` alanı zorunludur.")
		return in, false
	}
	defer func() { _ = file.Close() }()
	if header.Size > maxUploadBytes {
		problem(w, r, http.StatusRequestEntityTooLarge, "import/file-too-large", "IMPORT_FILE_TOO_LARGE",
			"Dosya çok büyük", fmt.Sprintf("En fazla %d MB yükleyebilirsiniz.", maxUploadBytes>>20))
		return in, false
	}
	content, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "Dosya okunamadı.")
		return in, false
	}
	if len(content) > maxUploadBytes {
		problem(w, r, http.StatusRequestEntityTooLarge, "import/file-too-large", "IMPORT_FILE_TOO_LARGE",
			"Dosya çok büyük", fmt.Sprintf("En fazla %d MB yükleyebilirsiniz.", maxUploadBytes>>20))
		return in, false
	}

	sponsor, err := uuid.Parse(r.FormValue("sponsorOrganizationId"))
	if err != nil {
		writeValidation(w, r, fieldErrors("sponsorOrganizationId", "FORMAT", "geçerli bir kimlik olmalı"))
		return in, false
	}
	in = memberimport.UploadInput{
		SponsorOrganizationID: sponsor,
		SourceSystem:          r.FormValue("sourceSystem"),
		SourceVersion:         r.FormValue("sourceVersion"),
		FileName:              header.Filename,
		Content:               content,
	}
	if raw := r.FormValue("planId"); raw != "" {
		planID, err := uuid.Parse(raw)
		if err != nil {
			writeValidation(w, r, fieldErrors("planId", "FORMAT", "geçerli bir kimlik olmalı"))
			return in, false
		}
		in.PlanID = planID
	}
	return in, true
}

// Get implements getMemberImport.
func (h *ImportHandler) Get(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "importId")
	if !ok {
		return
	}
	batch, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(batch.RowVersion))
	writeJSON(w, http.StatusOK, importBatchView(batch))
}

// List implements listMemberImports.
func (h *ImportHandler) List(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.List(r.Context(), rc, memberimport.ListFilter{
		Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	out := struct {
		Items      []kapsorav1.MemberImportBatch `json:"items"`
		NextCursor *string                       `json:"nextCursor,omitempty"`
	}{Items: make([]kapsorav1.MemberImportBatch, 0, len(page.Items))}
	for _, b := range page.Items {
		out.Items = append(out.Items, importBatchView(b))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

// ListRows implements listMemberImportRows.
func (h *ImportHandler) ListRows(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "importId")
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListRows(r.Context(), rc, id, memberimport.RowFilter{
		Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	out := struct {
		Items      []kapsorav1.MemberImportRow `json:"items"`
		NextCursor *string                     `json:"nextCursor,omitempty"`
	}{Items: make([]kapsorav1.MemberImportRow, 0, len(page.Items))}
	for _, row := range page.Items {
		out.Items = append(out.Items, importRowView(row))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

// Review implements reviewMemberImportRow.
func (h *ImportHandler) Review(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	batchID, ok := pathUUID(w, r, "importId")
	if !ok {
		return
	}
	rowID, ok := pathUUID(w, r, "rowId")
	if !ok {
		return
	}
	version, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewMemberImportRowJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	in := memberimport.ReviewInput{Decision: string(body.Decision), ExpectedVersion: version}
	if body.MatchedPersonId != nil {
		in.MatchedPersonID = *body.MatchedPersonId
	}
	row, err := h.svc.Review(r.Context(), rc, batchID, rowID, in)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(row.RowVersion))
	writeJSON(w, http.StatusOK, importRowView(row))
}

// Apply implements applyMemberImport: the accepted rows are written by the worker, so the
// call answers 202 with the batch in APPLYING. Requires step-up.
func (h *ImportHandler) Apply(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), PermissionImport)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionImport)
		return
	}
	id, ok := pathUUID(w, r, "importId")
	if !ok {
		return
	}
	version, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	batch, err := h.svc.Apply(r.Context(), rc, id, version)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(batch.RowVersion))
	writeJSON(w, http.StatusAccepted, importBatchView(batch))
}

// Cancel implements cancelMemberImport.
func (h *ImportHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "importId")
	if !ok {
		return
	}
	version, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReasonCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	batch, err := h.svc.Cancel(r.Context(), rc, id, version, body.ReasonCode)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(batch.RowVersion))
	writeJSON(w, http.StatusOK, importBatchView(batch))
}

// require resolves the request context or writes the audited denial.
func (h *ImportHandler) require(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), PermissionImport)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionImport)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// writeImportError maps the service errors to problem+json.
func (h *ImportHandler) writeImportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, memberimport.ErrNotFound), errors.Is(err, memberimport.ErrRowNotFound):
		problem(w, r, http.StatusNotFound, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, memberimport.ErrDuplicate):
		problem(w, r, http.StatusConflict, "import/duplicate", "IMPORT_DUPLICATE",
			"Bu dosya bu kaynak sürümüyle zaten yüklendi", "Yeni bir kaynak sürümü verin ya da mevcut partiyi kullanın.")
	case errors.Is(err, memberimport.ErrStateInvalid):
		problem(w, r, http.StatusConflict, "import/state-invalid", "IMPORT_STATE_INVALID",
			"Parti bu işleme uygun durumda değil", "")
	case errors.Is(err, memberimport.ErrRowNotReviewable):
		problem(w, r, http.StatusConflict, "import/row-not-reviewable", "IMPORT_ROW_NOT_REVIEWABLE",
			"Bu satır bu kararı alamaz", "")
	case errors.Is(err, memberimport.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp işlemi yeniden deneyin.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		h.writeError(w, r, err, false)
	}
}

// writeError reuses the person handler's validation and fallback mapping.
func (h *ImportHandler) writeError(w http.ResponseWriter, r *http.Request, err error, canSeeOwner bool) {
	(&Handler{logger: h.logger}).writeError(w, r, err, canSeeOwner)
}

// queryLimit reads and validates the page size.
func queryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeValidation(w, r, fieldErrors("limit", "FORMAT", "1-200 arası tam sayı olmalı"))
		return 0, false
	}
	return n, true
}

func importBatchView(b memberimport.Batch) kapsorav1.MemberImportBatch {
	out := kapsorav1.MemberImportBatch{
		Id:                    b.ID,
		SponsorOrganizationId: b.SponsorOrganizationID,
		SourceSystem:          b.SourceSystem,
		SourceVersion:         b.SourceVersion,
		FileName:              b.FileName,
		FileSha256:            b.FileSHA256,
		Format:                kapsorav1.MemberImportBatchFormat(b.Format),
		RowCount:              b.RowCount,
		Status:                kapsorav1.MemberImportBatchStatus(b.Status),
		CreatedAt:             b.CreatedAt,
		RowVersion:            int(b.RowVersion),
	}
	out.Counters.Valid = b.Counters.Valid
	out.Counters.Invalid = b.Counters.Invalid
	out.Counters.Matched = b.Counters.Matched
	out.Counters.Conflict = b.Counters.Conflict
	out.Counters.Created = b.Counters.Created
	out.Counters.Updated = b.Counters.Updated
	out.Counters.Skipped = b.Counters.Skipped
	if b.PlanID != nil {
		out.PlanId = b.PlanID
	}
	if b.ErrorSummary != nil {
		out.ErrorSummary = b.ErrorSummary
	}
	if b.AppliedAt != nil {
		out.AppliedAt = b.AppliedAt
	}
	return out
}

func importRowView(row memberimport.Row) kapsorav1.MemberImportRow {
	out := kapsorav1.MemberImportRow{
		Id:             row.ID,
		RowNo:          row.RowNo,
		SourceRecordId: row.SourceRecordID,
		Status:         kapsorav1.MemberImportRowStatus(row.Status),
		DisplayName:    row.DisplayName,
		Identifiers:    make([]kapsorav1.MaskedIdentifier, 0, len(row.Identifiers)),
		RowVersion:     int(row.RowVersion),
	}
	for _, id := range row.Identifiers {
		out.Identifiers = append(out.Identifiers, kapsorav1.MaskedIdentifier{
			Type: id.Type, MaskedValue: id.MaskedValue, Primary: false,
		})
	}
	out.Errors = make([]struct {
		Code    string  `json:"code"`
		Field   string  `json:"field"`
		Message *string `json:"message,omitempty"`
	}, 0, len(row.Errors))
	for _, e := range row.Errors {
		entry := struct {
			Code    string  `json:"code"`
			Field   string  `json:"field"`
			Message *string `json:"message,omitempty"`
		}{Code: e.Code, Field: e.Field}
		if e.Message != "" {
			message := e.Message
			entry.Message = &message
		}
		out.Errors = append(out.Errors, entry)
	}
	if row.BirthDate != nil {
		out.BirthDate = &openapi_types.Date{Time: dateOnly(*row.BirthDate)}
	}
	if row.MembershipType != nil {
		out.MembershipType = row.MembershipType
	}
	if row.PrincipalSourceRecordID != nil {
		out.PrincipalSourceRecordId = row.PrincipalSourceRecordID
	}
	if row.PlanCode != nil {
		out.PlanCode = row.PlanCode
	}
	if row.MatchedPersonID != nil {
		out.MatchedPersonId = row.MatchedPersonID
	}
	if len(row.CandidatePersonIDs) > 0 {
		ids := make([]openapi_types.UUID, 0, len(row.CandidatePersonIDs))
		ids = append(ids, row.CandidatePersonIDs...)
		out.CandidatePersonIds = &ids
	}
	if row.Decision != nil {
		decision := kapsorav1.MemberImportRowDecision(*row.Decision)
		out.Decision = &decision
	}
	if row.AppliedPersonID != nil {
		out.AppliedPersonId = row.AppliedPersonID
	}
	return out
}

// fieldErrors builds the one-entry field error list the validation writer expects.
func fieldErrors(field, code, message string) []domain.FieldError {
	return []domain.FieldError{{Field: field, Code: code, Message: message}}
}
