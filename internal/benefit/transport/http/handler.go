// Package benefithttp serves /api/v1/programs, /plans, /plan-versions, /enrollments and
// the person-scoped enrollment routes. Bodies use the generated contract types where the
// contract has no decimal field; errors are problem+json with stable codes (ADR-015).
package benefithttp

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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes (migration 000008).
const (
	PermissionProgramRead    = "program.read"
	PermissionProgramManage  = "program.manage"
	PermissionPlanManage     = "plan.manage"
	PermissionPlanPublish    = "plan.publish"
	PermissionEnrollManage   = "enrollment.manage"
	maxBodyBytes             = 256 << 10
	mergePatchContentType    = "application/merge-patch+json"
	defaultProblemNotFoundID = "generic/not-found"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the idempotency
// middleware on the four create commands. A nil field means "no wrapper".
type Middlewares struct {
	CreateProgram     func(http.Handler) http.Handler
	CreatePlan        func(http.Handler) http.Handler
	CreatePlanVersion func(http.Handler) http.Handler
	CreateEnrollment  func(http.Handler) http.Handler
}

// Handler serves the benefit operations.
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

// ProgramRoutes mounts everything below /programs.
func (h *Handler) ProgramRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListPrograms)
	r.With(wrap(mw.CreateProgram)).Post("/", h.CreateProgram)
	r.Get("/{programId}", h.GetProgram)
	r.Patch("/{programId}", h.UpdateProgram)
	r.Get("/{programId}/plans", h.ListPlans)
	r.With(wrap(mw.CreatePlan)).Post("/{programId}/plans", h.CreatePlan)
}

// PlanRoutes mounts everything below /plans.
func (h *Handler) PlanRoutes(r chi.Router, mw Middlewares) {
	r.Get("/{planId}", h.GetPlan)
	r.Patch("/{planId}", h.UpdatePlan)
	r.Get("/{planId}/versions", h.ListPlanVersions)
	r.With(wrap(mw.CreatePlanVersion)).Post("/{planId}/versions", h.CreatePlanVersion)
}

// PlanVersionRoutes mounts everything below /plan-versions.
func (h *Handler) PlanVersionRoutes(r chi.Router) {
	r.Get("/{planVersionId}", h.GetPlanVersion)
	r.Patch("/{planVersionId}", h.UpdatePlanVersion)
	r.Put("/{planVersionId}/entitlement-definitions", h.ReplaceDefinitions)
	r.Post("/{planVersionId}/submit", h.SubmitPlanVersion)
	r.Post("/{planVersionId}/publish", h.PublishPlanVersion)
	r.Post("/{planVersionId}/retire", h.RetirePlanVersion)
}

// EnrollmentRoutes mounts everything below /enrollments.
func (h *Handler) EnrollmentRoutes(r chi.Router) {
	r.Get("/", h.ListEnrollments)
	r.Get("/{enrollmentId}", h.GetEnrollment)
	r.Patch("/{enrollmentId}", h.UpdateEnrollment)
}

// PersonRoutes mounts the person-scoped enrollment routes; the router already serves the
// rest of /people from the party module.
func (h *Handler) PersonRoutes(r chi.Router, mw Middlewares) {
	r.Get("/{personId}/enrollments", h.ListPersonEnrollments)
	r.With(wrap(mw.CreateEnrollment)).Post("/{personId}/enrollments", h.CreateEnrollment)
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

// requireStepUp is require plus a valid step-up window (publish and retire).
func (h *Handler) requireStepUp(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.RequireStepUp(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	writeBenefitError(h.logger, w, r, err)
}

// writeBenefitError is the shared mapping of the module's errors; the entitlement
// handler maps its own ledger errors first and falls back to this one.
func writeBenefitError(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrProgramNotFound):
		problem(w, r, http.StatusNotFound, "benefit/program-not-found", "PROGRAM_NOT_FOUND", "Program bulunamadı", "")
	case errors.Is(err, application.ErrPlanNotFound):
		problem(w, r, http.StatusNotFound, "benefit/plan-not-found", "PLAN_NOT_FOUND", "Plan bulunamadı", "")
	case errors.Is(err, application.ErrPlanVersionNotFound):
		problem(w, r, http.StatusNotFound, "benefit/plan-version-not-found", "PLAN_VERSION_NOT_FOUND", "Plan sürümü bulunamadı", "")
	case errors.Is(err, application.ErrEnrollmentNotFound):
		problem(w, r, http.StatusNotFound, "benefit/enrollment-not-found", "ENROLLMENT_NOT_FOUND", "Kayıt bulunamadı", "")
	case errors.Is(err, application.ErrNotFound):
		problem(w, r, http.StatusNotFound, defaultProblemNotFoundID, "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, application.ErrProgramCodeTaken):
		problem(w, r, http.StatusConflict, "benefit/program-code-taken", "PROGRAM_CODE_TAKEN", "Bu program kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrPlanCodeTaken):
		problem(w, r, http.StatusConflict, "benefit/plan-code-taken", "PLAN_CODE_TAKEN", "Bu plan kodu programda zaten kullanılıyor", "")
	case errors.Is(err, application.ErrProgramTransition):
		problem(w, r, http.StatusConflict, "benefit/program-transition-invalid", "PROGRAM_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, application.ErrPlanTransition):
		problem(w, r, http.StatusConflict, "benefit/plan-transition-invalid", "PLAN_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, application.ErrVersionImmutable):
		problem(w, r, http.StatusConflict, "benefit/plan-version-immutable", "PLAN_VERSION_IMMUTABLE",
			"Yayınlanmış plan sürümü değiştirilemez", "")
	case errors.Is(err, application.ErrVersionOverlap):
		problem(w, r, http.StatusConflict, "benefit/plan-version-overlap", "PLAN_VERSION_OVERLAP",
			"Bu plan için çakışan yayınlanmış bir dönem var", "")
	case errors.Is(err, application.ErrVersionTransition), errors.Is(err, application.ErrVersionNumberTaken):
		problem(w, r, http.StatusConflict, "benefit/plan-version-transition-invalid", "PLAN_VERSION_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, application.ErrMakerCheckerSame):
		problem(w, r, http.StatusForbidden, "benefit/maker-checker-same-actor", "MAKER_CHECKER_SAME_ACTOR",
			"Yayınlayan, gönderenden farklı olmalı", "Sürümü incelemeye gönderen kullanıcı onu yayınlayamaz.")
	case errors.Is(err, application.ErrEnrollmentOverlap):
		problem(w, r, http.StatusConflict, "benefit/enrollment-overlap", "ENROLLMENT_OVERLAP",
			"Bu üyelik ve plan için çakışan bir dönem var", "")
	case errors.Is(err, application.ErrEnrollmentTransition):
		problem(w, r, http.StatusConflict, "benefit/enrollment-transition-invalid", "ENROLLMENT_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		logger.Error("benefit request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}

// pathUUID reads a path parameter; a malformed id is indistinguishable from an unknown one.
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

// requireMergePatch enforces the merge-patch media type of PATCH endpoints.
func requireMergePatch(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), mergePatchContentType) {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type", "UNSUPPORTED_MEDIA_TYPE",
			"Content-Type application/merge-patch+json olmalı", "")
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// decodeOptionalJSON accepts an empty body (the review commands have an optional body).
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// isJSONNull recognises an explicit null (merge-patch: "remove this field").
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

func decodeString(raw json.RawMessage, field string, fields *[]domain.FieldError) *string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	return &s
}

func decodeDate(raw json.RawMessage, field string, fields *[]domain.FieldError) *time.Time {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "tarih metni olmalı"})
		return nil
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return nil
	}
	return &d
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

// dateOnly strips the clock from a contract date so comparisons stay day-based.
func dateOnly(t time.Time) time.Time { return domain.DateOnly(t) }

// queryLimit reads the paging limit; a malformed value falls back to the default.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}
