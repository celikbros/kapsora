// Package ruleshttp serves /api/v1/rule-sets, /rule-set-versions and /rule-evaluations.
// Bodies use the generated contract types; errors are problem+json with stable codes
// (ADR-015). The evaluator is never reached from here directly: the application layer owns
// compilation, the publish gate and what may be recorded.
package ruleshttp

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

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// Permissions guarding the routes (migration 000008, RULE_AUTHOR and RULE_APPROVER role
// templates). Publishing and retiring take the third one and a recent step-up, because a
// published rule set is what a member's claim is decided against.
const (
	PermissionRead    = application.PermissionRead
	PermissionDraft   = application.PermissionDraft
	PermissionPublish = application.PermissionPublish
)

const (
	maxBodyBytes          = 1 << 20
	mergePatchContentType = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the idempotency
// middleware on the create commands. A nil field means "no wrapper".
type Middlewares struct {
	CreateRuleSet        func(http.Handler) http.Handler
	CreateRuleSetVersion func(http.Handler) http.Handler
}

// Handler serves the rule engine operations.
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

// RuleSetRoutes mounts everything below /rule-sets.
func (h *Handler) RuleSetRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListRuleSets)
	r.With(wrap(mw.CreateRuleSet)).Post("/", h.CreateRuleSet)
	r.Get("/{ruleSetId}", h.GetRuleSet)
	r.Patch("/{ruleSetId}", h.PatchRuleSet)
	r.Get("/{ruleSetId}/versions", h.ListRuleSetVersions)
	r.With(wrap(mw.CreateRuleSetVersion)).Post("/{ruleSetId}/versions", h.CreateRuleSetVersion)
}

// VersionRoutes mounts everything below /rule-set-versions. Versions are addressed
// directly rather than under their set, because a UI reaches one from a version list or
// from an evaluation rather than by walking down from the set.
//
// tests:run and :simulate keep the colon form v1.2 uses for a command that is a question
// rather than a sub-resource: neither of them creates anything, and neither has a URL of
// its own to be fetched later.
func (h *Handler) VersionRoutes(r chi.Router) {
	r.Get("/{ruleSetVersionId}", h.GetRuleSetVersion)
	r.Patch("/{ruleSetVersionId}", h.PatchRuleSetVersion)
	r.Put("/{ruleSetVersionId}/rules", h.PutRules)
	r.Put("/{ruleSetVersionId}/test-cases", h.PutRuleTestCases)
	r.Post("/{ruleSetVersionId}/tests:run", h.RunRuleTests)
	r.Post("/{ruleSetVersionId}:simulate", h.SimulateRuleSetVersion)
	r.Post("/{ruleSetVersionId}/submit", h.SubmitRuleSetVersion)
	r.Post("/{ruleSetVersionId}/publish", h.PublishRuleSetVersion)
	r.Post("/{ruleSetVersionId}/retire", h.RetireRuleSetVersion)
}

// EvaluationRoutes mounts everything below /rule-evaluations.
func (h *Handler) EvaluationRoutes(r chi.Router) {
	r.Get("/{ruleEvaluationId}", h.GetRuleEvaluation)
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
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrRuleSetNotFound):
		problem(w, r, http.StatusNotFound, "rules/rule-set-not-found", "RULE_SET_NOT_FOUND",
			"Kural seti bulunamadı", "")
	case errors.Is(err, application.ErrVersionNotFound):
		problem(w, r, http.StatusNotFound, "rules/version-not-found", "RULE_SET_VERSION_NOT_FOUND",
			"Kural seti sürümü bulunamadı", "")
	case errors.Is(err, application.ErrEvaluationNotFound):
		problem(w, r, http.StatusNotFound, "rules/evaluation-not-found", "RULE_EVALUATION_NOT_FOUND",
			"Kural değerlendirmesi bulunamadı", "")
	case errors.Is(err, application.ErrRuleSetCodeTaken):
		problem(w, r, http.StatusConflict, "rules/rule-set-code-taken", "RULE_SET_CODE_TAKEN",
			"Bu kural seti kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrRuleCodeTaken):
		problem(w, r, http.StatusConflict, "rules/rule-code-taken", "RULE_CODE_TAKEN",
			"Bu kural kodu bu sürümde kullanılıyor", "")
	case errors.Is(err, application.ErrRulePriorityTaken):
		problem(w, r, http.StatusConflict, "rules/rule-priority-taken", "RULE_PRIORITY_TAKEN",
			"İki kural aynı önceliği paylaşamaz",
			"Değerlendirme sırası kesin olmalı; her kurala farklı bir öncelik verin.")
	case errors.Is(err, application.ErrTestCaseCodeTaken):
		problem(w, r, http.StatusConflict, "rules/test-case-code-taken", "RULE_TEST_CASE_CODE_TAKEN",
			"Bu test senaryosu kodu bu sürümde kullanılıyor", "")
	case errors.Is(err, application.ErrVersionImmutable):
		problem(w, r, http.StatusConflict, "rules/version-immutable", "RULE_VERSION_IMMUTABLE",
			"Yayınlanmış kural sürümü değiştirilemez",
			"Bu sürümle verilmiş kararlar var; yeni bir sürüm açın.")
	case errors.Is(err, application.ErrVersionOverlap):
		problem(w, r, http.StatusConflict, "rules/version-overlap", "RULE_SET_VERSION_OVERLAP",
			"Bu tarih aralığında yayında başka bir sürüm var",
			"Bir kural setinin iki yayınlanmış sürümü aynı günü kapsayamaz.")
	case errors.Is(err, application.ErrVersionTransition):
		problem(w, r, http.StatusConflict, "rules/version-transition-invalid", "RULE_VERSION_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, application.ErrVersionNotLive):
		problem(w, r, http.StatusConflict, "rules/version-not-published", "RULE_VERSION_NOT_PUBLISHED",
			"Yalnız yayınlanmış bir sürüm karar kaydı üretebilir",
			"Taslak bir sürümü denemek için simülasyonu kullanın.")
	case errors.Is(err, application.ErrMakerCheckerSame):
		problem(w, r, http.StatusForbidden, "rules/maker-checker-same-actor", "MAKER_CHECKER_SAME_ACTOR",
			"Yayınlayan, gönderenden farklı olmalı", "Sürümü incelemeye gönderen kullanıcı onu yayınlayamaz.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		// A rule error never carries personal data: the module stores codes, ids, dates
		// and quantities only, and the input snapshot is filtered before it is written.
		h.logger.Error("rules request failed", "error", err)
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, r, http.StatusRequestEntityTooLarge, "generic/request-body-too-large", "REQUEST_BODY_TOO_LARGE",
				"İstek gövdesi çok büyük", "")
			return false
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// decodeOptionalJSON accepts an empty body, which submit and publish allow.
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

func decodeSchema(raw json.RawMessage, field string, fields *[]domain.FieldError) map[string]string {
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: field, Code: "TYPE", Message: "değişken adı ve CEL türünden oluşan bir nesne olmalı",
		})
		return nil
	}
	return out
}

// immutable is the answer to a patch that carries a field this module never rewrites.
func immutable(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{Field: field, Code: "IMMUTABLE", Message: "bu alan değiştirilemez"})
}

func unknownField(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{Field: field, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
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
