// Package workflowhttp serves /api/v1/work-queues, /api/v1/work-items and
// /api/v1/approval-policies. Bodies use the generated contract types; errors are
// problem+json with stable codes (ADR-015).
//
// Two things shape the routes here. Every command on an item is a route of its own with
// its own permission — taking work, putting it down, taking it off somebody else, saying
// what was decided — because "who may do this" is then a routing fact rather than a branch
// in a body. And every one of them carries If-Match: an item is a thing two people reach
// for at the same time, and a command with no version is a command that can silently
// overwrite whoever got there first.
package workflowhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// Permissions guarding the routes (migration 000027). Taking a piece of work and taking it
// off somebody else are separate grants on purpose: a reviewer picks up their own work, a
// supervisor moves other people's.
const (
	PermissionRead        = application.PermissionRead
	PermissionClaim       = application.PermissionClaim
	PermissionReassign    = application.PermissionReassign
	PermissionQueueManage = application.PermissionQueueManage
	PermissionPolicy      = application.PermissionPolicy
)

// queueReadPermissions are the grants that may read the queue definitions. Anybody who may
// see a worklist may see the queues it is drawn from; inventing a separate read grant
// would only mean a reviewer who can open their list but not name the queue it came from.
var queueReadPermissions = []string{
	application.PermissionQueueManage, application.PermissionRead,
}

const (
	maxBodyBytes          = 1 << 20
	mergePatchContentType = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateQueue  func(http.Handler) http.Handler
	ClaimItem    func(http.Handler) http.Handler
	ReleaseItem  func(http.Handler) http.Handler
	ReassignItem func(http.Handler) http.Handler
	CompleteItem func(http.Handler) http.Handler
	AddComment   func(http.Handler) http.Handler
	PutPolicies  func(http.Handler) http.Handler
}

// Handler serves the work queue, work item, comment and approval policy operations.
type Handler struct {
	svc    *application.Service
	deny   Denier
	logger *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(svc *application.Service, deny Denier, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, deny: deny, logger: logger}
}

// QueueRoutes mounts everything below /work-queues.
func (h *Handler) QueueRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListWorkQueues)
	r.With(wrap(mw.CreateQueue)).Post("/", h.CreateWorkQueue)
	r.Patch("/{queueId}", h.PatchWorkQueue)
}

// ItemRoutes mounts everything below /work-items. Each command is a route of its own
// rather than a verb in a body, which is what makes "who may do this" a routing fact.
func (h *Handler) ItemRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListWorkItems)
	r.Get("/{workItemId}", h.GetWorkItem)
	r.With(wrap(mw.ClaimItem)).Post("/{workItemId}/claim", h.ClaimWorkItem)
	r.With(wrap(mw.ReleaseItem)).Post("/{workItemId}/release", h.ReleaseWorkItem)
	r.With(wrap(mw.ReassignItem)).Post("/{workItemId}/reassign", h.ReassignWorkItem)
	r.With(wrap(mw.CompleteItem)).Post("/{workItemId}/complete", h.CompleteWorkItem)
	r.Get("/{workItemId}/comments", h.ListWorkItemComments)
	r.With(wrap(mw.AddComment)).Post("/{workItemId}/comments", h.AddWorkItemComment)
}

// PolicyRoutes mounts everything below /approval-policies.
func (h *Handler) PolicyRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListApprovalPolicies)
	r.With(wrap(mw.PutPolicies)).Put("/", h.PutApprovalPolicies)
}

func wrap(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mw == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return mw
}

// require resolves the request context or writes the denial through the auditing denier.
func (h *Handler) require(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// requireAny resolves the request context for a caller holding any of the permissions.
// The denial is audited against the first of them, which is the one the endpoint is really
// about; a caller holding none of them is refused exactly as if only that one existed.
func (h *Handler) requireAny(w http.ResponseWriter, r *http.Request, permissions []string) (identity.RequestContext, bool) {
	var lastErr error
	for _, permission := range permissions {
		rc, err := identity.Require(r.Context(), permission)
		if err == nil {
			return rc, true
		}
		lastErr = err
		// An unauthenticated caller holds nothing at all; trying the rest would only
		// produce the same answer with a different permission name on it.
		if errors.Is(err, identity.ErrUnauthenticated) {
			break
		}
	}
	h.deny.Deny(w, r, lastErr, permissions[0])
	return identity.RequestContext{}, false
}

// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	var claimed *application.AlreadyClaimedError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.As(err, &claimed):
		// The detail names the actor who won, and the extension members carry the same
		// fact in a form a screen can use without parsing a sentence. Telling somebody the
		// item is taken without saying by whom is what makes two people keep clicking.
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "work-items/already-claimed",
			Title:  "İş kalemi başkası tarafından üstlenilmiş",
			Status: http.StatusConflict, Code: "WORK_ITEM_ALREADY_CLAIMED",
			Detail: claimedDetail(claimed), Extensions: claimedExtensions(claimed),
		})
	case errors.Is(err, application.ErrQueueNotFound):
		problem(w, r, http.StatusNotFound, "work-queues/not-found", "WORK_QUEUE_NOT_FOUND",
			"İş kuyruğu bulunamadı", "")
	case errors.Is(err, application.ErrWorkItemNotFound):
		problem(w, r, http.StatusNotFound, "work-items/not-found", "WORK_ITEM_NOT_FOUND",
			"İş kalemi bulunamadı", "")
	case errors.Is(err, application.ErrPolicyNotFound):
		problem(w, r, http.StatusNotFound, "approval-policies/not-found", "APPROVAL_POLICY_NOT_FOUND",
			"Bu işlem ve tutar için onay politikası yok", "")
	case errors.Is(err, application.ErrQueueCodeTaken):
		problem(w, r, http.StatusConflict, "work-queues/code-taken", "WORK_QUEUE_CODE_TAKEN",
			"Bu kuyruk kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrQueueInactive):
		problem(w, r, http.StatusConflict, "work-queues/inactive", "WORK_QUEUE_INACTIVE",
			"Kuyruk kapalı", "Kapalı bir kuyruğa yeni iş düşürülemez.")
	case errors.Is(err, application.ErrEscalationCycle):
		problem(w, r, http.StatusUnprocessableEntity, "work-queues/escalation-cycle",
			"WORK_QUEUE_ESCALATION_CYCLE", "Bir kuyruk kendine yönlendirilemez", "")
	case errors.Is(err, application.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "work-items/transition-invalid",
			"WORK_ITEM_TRANSITION_INVALID", "İş kalemi bu duruma geçemez", "")
	case errors.Is(err, application.ErrNotAssignee):
		problem(w, r, http.StatusConflict, "work-items/not-assignee", "WORK_ITEM_NOT_ASSIGNEE",
			"Bu iş kalemi sizde değil",
			"Başkasının üzerindeki işi almak için yeniden atama yetkisi gerekir.")
	case errors.Is(err, application.ErrPolicyOverlap):
		problem(w, r, http.StatusConflict, "approval-policies/overlap", "APPROVAL_POLICY_OVERLAP",
			"Aynı işlem ve kapsam için çakışan dönem var", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A worklist error carries no personal data: this module stores ids, codes,
		// timestamps and counts, and a comment body never reaches an error at all.
		h.logger.Error("workflow command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// claimedDetail renders who holds the item. The actor id is the only thing said about
// them: a display name would put a person's name in a problem body that is logged wherever
// the caller logs its errors.
func claimedDetail(err *application.AlreadyClaimedError) string {
	switch {
	case err.AssigneeActorID == nil:
		return "İş kalemi artık üstlenilebilir durumda değil."
	case err.AssigneeDisplayName != nil && *err.AssigneeDisplayName != "":
		return fmt.Sprintf("İş kalemi %s kullanıcısında.", *err.AssigneeDisplayName)
	default:
		return fmt.Sprintf("İş kalemi %s kimlikli kullanıcıda.", err.AssigneeActorID)
	}
}

// claimedExtensions is the machine-readable half of the same answer (WP-I5-05 section
// 2.6). The members are absent rather than null when there is nobody to name: an item
// that went back to OPEN between the failed update and the re-read was taken by no one.
func claimedExtensions(err *application.AlreadyClaimedError) map[string]any {
	if err.AssigneeActorID == nil {
		return nil
	}
	out := map[string]any{"assigneeActorId": err.AssigneeActorID.String()}
	if err.AssigneeDisplayName != nil && *err.AssigneeDisplayName != "" {
		out["assigneeDisplayName"] = *err.AssigneeDisplayName
	}
	return out
}

// pathUUID reads an id from the path; a malformed id is indistinguishable from an unknown
// one, so both are answered the same way.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
}

func etag(version int64) string { return fmt.Sprintf(`"%d"`, version) }

// requireIfMatch parses the If-Match header, answering 428 when it is missing.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || n < 1 {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED",
			"If-Match başlığı gerekli", "GET yanıtındaki ETag değerini If-Match olarak gönderin.")
		return 0, false
	}
	return n, true
}

// requireMergePatch enforces the merge-patch media type of the PATCH endpoint.
func requireMergePatch(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), mergePatchContentType) {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type",
			"UNSUPPORTED_MEDIA_TYPE", "Content-Type application/merge-patch+json olmalı", "")
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, r, http.StatusRequestEntityTooLarge, "generic/request-body-too-large",
				"REQUEST_BODY_TOO_LARGE", "İstek gövdesi çok büyük", "")
			return false
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// decodeOptionalJSON accepts an empty body, which release allows.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// isJSONNull recognises an explicit null (merge-patch: "remove this field").
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

func writeValidation(w http.ResponseWriter, r *http.Request, fields []domain.FieldError) {
	p := httpx.Problem{
		Type: httpx.ProblemTypeBase + "generic/validation-failed", Title: "Doğrulama hatası",
		Status: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED",
	}
	for _, f := range fields {
		p.Errors = append(p.Errors, httpx.FieldError{Field: f.Field, Code: f.Code, Message: f.Message})
	}
	httpx.WriteProblem(w, r, p)
}

func problem(w http.ResponseWriter, r *http.Request, status int, typ, code, title, detail string) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type: httpx.ProblemTypeBase + typ, Title: title, Status: status, Code: code, Detail: detail,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// queryLimit reads the paging limit; a malformed value falls back to the default.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

// queryUUID reads an optional uuid filter; a malformed one is a field error rather than a
// silently empty page.
func queryUUID(r *http.Request, name string, fields *[]domain.FieldError) *uuid.UUID {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "geçerli bir kimlik olmalı"})
		return nil
	}
	return &id
}

// queryBool reads an optional tri-state filter: absent means "do not filter", which is a
// different question from "false".
func queryBool(r *http.Request, name string, fields *[]domain.FieldError) *bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "TYPE", Message: "true veya false olmalı"})
		return nil
	}
	return &value
}

func decodeString(raw json.RawMessage, field string, fields *[]domain.FieldError) *string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	return &s
}

func decodeBool(raw json.RawMessage, field string, fields *[]domain.FieldError) *bool {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "true veya false olmalı"})
		return nil
	}
	return &b
}

// decodeNullableInt returns the two levels a merge patch needs: the outer pointer says the
// field was mentioned, the inner one carries the value or the explicit null.
func decodeNullableInt(raw json.RawMessage, field string, fields *[]domain.FieldError) **int {
	if isJSONNull(raw) {
		var cleared *int
		return &cleared
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "tam sayı olmalı"})
		return nil
	}
	value := &n
	return &value
}

// decodeNullableUUID is decodeNullableInt for an id, kept as text: the service parses it
// once, and an unparseable one is a field error here rather than a silent nil there.
func decodeNullableUUID(raw json.RawMessage, field string, fields *[]domain.FieldError) **string {
	if isJSONNull(raw) {
		var cleared *string
		return &cleared
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "geçerli bir kimlik olmalı"})
		return nil
	}
	value := &s
	return &value
}
