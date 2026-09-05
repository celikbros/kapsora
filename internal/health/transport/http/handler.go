// Package healthhttp serves /api/v1/health-cases, /api/v1/encounters and
// /api/v1/health-access-log. Bodies use the generated contract types; errors are
// problem+json with stable codes (ADR-015).
//
// One thing shapes every route here: **no handler in this package decides what a caller may
// see**. The permission the route names is the permission to reach the endpoint at all; the
// projection is applied in the application service, on the record, and the mappers below can
// only render what they were given. A field the service cleared is absent from the body
// because it is absent from the record, not because a mapper remembered to leave it out.
//
// The one place a handler does read a permission is listEncounterDiagnoses, and it reads it
// to produce a refusal rather than a narrower body: there is no financial projection of a
// diagnosis, and an empty list would be an answer about the patient.
package healthhttp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. Reading a case and reading its clinical detail are two
// grants on purpose: the first is what a sponsor's HR user has, and the second is what it
// deliberately does not.
const (
	PermissionCaseRead     = application.PermissionCaseRead
	PermissionCaseManage   = application.PermissionCaseManage
	PermissionClinicalRead = application.PermissionClinicalRead
	PermissionAuditRead    = application.PermissionAuditRead
)

const maxBodyBytes = 1 << 20

// Headers a clinical read may state its reason in.
const (
	headerAccessPurpose = "X-Access-Purpose"
	headerAccessReason  = "X-Access-Reason"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateCase      func(http.Handler) http.Handler
	CloseCase       func(http.Handler) http.Handler
	CreateEncounter func(http.Handler) http.Handler
	PutDiagnoses    func(http.Handler) http.Handler
}

// Handler serves the health case operations.
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

// CaseRoutes mounts everything below /health-cases.
func (h *Handler) CaseRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListHealthCases)
	r.With(wrap(mw.CreateCase)).Post("/", h.CreateHealthCase)
	r.Get("/{caseId}", h.GetHealthCase)
	r.With(wrap(mw.CloseCase)).Post("/{caseId}/close", h.CloseHealthCase)
	r.With(wrap(mw.CreateEncounter)).Post("/{caseId}/encounters", h.CreateEncounter)
}

// EncounterRoutes mounts everything below /encounters.
func (h *Handler) EncounterRoutes(r chi.Router, mw Middlewares) {
	r.Get("/{encounterId}", h.GetEncounter)
	r.Get("/{encounterId}/diagnoses", h.ListEncounterDiagnoses)
	r.With(wrap(mw.PutDiagnoses)).Put("/{encounterId}/diagnoses", h.PutEncounterDiagnoses)
}

// AccessLogRoutes mounts /health-access-log.
func (h *Handler) AccessLogRoutes(r chi.Router) {
	r.Get("/", h.ListHealthAccessLog)
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

// accessRequest reads the two headers a clinical read may state its reason in. Neither is
// validated here: the service checks the purpose against the reference table, so there is
// one authority rather than a copy of the list in the transport.
//
// The reason is percent-decoded because an HTTP header value is ISO-8859-1 and a browser
// refuses to send one containing ğ, ş or ı — which is most of the Turkish a person would
// actually type. Go would have accepted the raw bytes and the web client could never have
// produced them, so the contract says the value is percent-encoded UTF-8 and both sides
// encode it. A value with no percent sequences decodes to itself, so a plain ASCII reason
// still works untouched.
func accessRequest(r *http.Request) application.AccessRequest {
	return application.AccessRequest{
		PurposeCode: strings.TrimSpace(r.Header.Get(headerAccessPurpose)),
		ReasonText:  decodeReason(r.Header.Get(headerAccessReason)),
	}
}

// decodeReason percent-decodes the access reason, falling back to the raw value when it is
// not valid percent-encoding: a caller that sent a bare "50% indirim" meant those bytes, and
// refusing the whole read over the punctuation in a reason would be an odd way to insist on
// having one.
func decodeReason(raw string) string {
	trimmed := strings.TrimSpace(raw)
	decoded, err := url.QueryUnescape(trimmed)
	if err != nil {
		return trimmed
	}
	return strings.TrimSpace(decoded)
}

// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrCaseNotFound):
		problem(w, r, http.StatusNotFound, "health-cases/not-found", "HEALTH_CASE_NOT_FOUND",
			"Sağlık vakası bulunamadı", "")
	case errors.Is(err, application.ErrEncounterNotFound):
		problem(w, r, http.StatusNotFound, "encounters/not-found", "ENCOUNTER_NOT_FOUND",
			"Encounter bulunamadı", "")
	case errors.Is(err, application.ErrRequestNotFound):
		problem(w, r, http.StatusNotFound, "health-cases/request-not-found",
			"SERVICE_REQUEST_NOT_FOUND", "Hizmet talebi bulunamadı", "")
	case errors.Is(err, application.ErrClinicalReadRequired):
		// 403 rather than an empty list. An empty list would say the encounter has no
		// diagnosis, which is a clinical fact the caller does not hold the grant for; and
		// the same answer covers a sensitive case it may not read, so the refusal itself
		// never says which of the two it was.
		problem(w, r, http.StatusForbidden, "health/clinical-read-required", "CLINICAL_READ_REQUIRED",
			"Klinik detay için ek yetki gerekiyor",
			"Tanı bilgisini görmek için klinik okuma yetkisi gerekir.")
	case errors.Is(err, application.ErrAccessPurposeRequired):
		problem(w, r, http.StatusPreconditionRequired, "health/access-purpose-required",
			"ACCESS_PURPOSE_REQUIRED", "Erişim amacı belirtilmeli",
			"Bu kaydı görüntülemek için X-Access-Purpose başlığıyla erişim amacınızı bildirin.")
	case errors.Is(err, application.ErrCaseClosed):
		problem(w, r, http.StatusConflict, "health-cases/closed", "HEALTH_CASE_CLOSED",
			"Sağlık vakası kapalı", "Kapanmış bir vakaya kayıt eklenemez.")
	case errors.Is(err, application.ErrEncounterOpen):
		problem(w, r, http.StatusConflict, "health-cases/encounter-open", "HEALTH_CASE_ENCOUNTER_OPEN",
			"Vakada bitmemiş bir encounter var", "Vakayı kapatmadan önce tüm encounter'ları sonlandırın.")
	case errors.Is(err, application.ErrStayOpen):
		problem(w, r, http.StatusConflict, "health-cases/stay-open", "HEALTH_CASE_STAY_OPEN",
			"Vakada açık bir yatış var", "Vakayı kapatmadan önce yatışı taburcu edin.")
	case errors.Is(err, application.ErrEnrollmentMismatch):
		problem(w, r, http.StatusUnprocessableEntity, "health-cases/enrollment-mismatch",
			"HEALTH_CASE_ENROLLMENT_MISMATCH", "Plan kaydı bu kişiye veya programa ait değil", "")
	case errors.Is(err, application.ErrRequestNotEligible):
		problem(w, r, http.StatusUnprocessableEntity, "health-cases/request-not-eligible",
			"HEALTH_CASE_REQUEST_NOT_ELIGIBLE", "Bu talepten sağlık vakası açılamaz",
			"Vaka yalnızca sağlık alanındaki doğrudan hizmet ya da ön onay talebinden açılabilir.")
	case errors.Is(err, application.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "health-cases/provider-scope",
			"HEALTH_CASE_PROVIDER_SCOPE", "Bu sağlayıcı adına işlem yapamazsınız", "")
	case errors.Is(err, application.ErrProviderUnknown):
		problem(w, r, http.StatusUnprocessableEntity, "health-cases/provider-unknown",
			"HEALTH_CASE_PROVIDER_UNKNOWN", "Bu kurum tenant'ın sağlayıcısı değil", "")
	case errors.Is(err, application.ErrReportNotFound):
		problem(w, r, http.StatusNotFound, "medical-reports/not-found", "MEDICAL_REPORT_NOT_FOUND",
			"Tedavi raporu bulunamadı", "")
	case errors.Is(err, application.ErrReportImmutable):
		// The freeze the whole package exists for. 409 rather than 403: the caller is
		// allowed to write reports, and this one has simply stopped being writable.
		problem(w, r, http.StatusConflict, "medical-reports/immutable", "MEDICAL_REPORT_IMMUTABLE",
			"Karara bağlanmış rapor değiştirilemez",
			"Düzeltme için raporun yeni bir sürümünü oluşturun.")
	case errors.Is(err, application.ErrReportTransitionInvalid):
		problem(w, r, http.StatusConflict, "medical-reports/transition-invalid",
			"MEDICAL_REPORT_TRANSITION_INVALID", "Rapor bu durumda bu işleme uygun değil", "")
	case errors.Is(err, application.ErrReportChainApproved):
		problem(w, r, http.StatusConflict, "medical-reports/chain-approved",
			"MEDICAL_REPORT_CHAIN_APPROVED", "Bu raporun onaylı bir sürümü zaten var", "")
	case errors.Is(err, application.ErrReportServiceRequired):
		problem(w, r, http.StatusUnprocessableEntity, "medical-reports/service-required",
			"MEDICAL_REPORT_SERVICE_REQUIRED", "Raporda en az bir hizmet satırı olmalı", "")
	case errors.Is(err, application.ErrReportDocumentRequired):
		problem(w, r, http.StatusUnprocessableEntity, "medical-reports/document-required",
			"MEDICAL_REPORT_DOCUMENT_REQUIRED", "Rapor belgesi yüklenmeli",
			"Gönderimden önce rapor dosyasını yükleyin ve taramanın tamamlanmasını bekleyin.")
	case errors.Is(err, application.ErrReportSupersedesInvalid):
		problem(w, r, http.StatusUnprocessableEntity, "medical-reports/supersedes-invalid",
			"MEDICAL_REPORT_SUPERSEDES_INVALID", "Bu rapor düzeltilemez",
			"Yalnızca bir zincirin en son karara bağlanmış sürümü düzeltilebilir.")
	case errors.Is(err, application.ErrReportServiceUnknown):
		problem(w, r, http.StatusUnprocessableEntity, "medical-reports/service-unknown",
			"MEDICAL_REPORT_SERVICE_UNKNOWN", "Hizmet tanımı katalogda bulunamadı", "")
	case errors.Is(err, application.ErrReportCaseMismatch):
		problem(w, r, http.StatusUnprocessableEntity, "medical-reports/case-mismatch",
			"MEDICAL_REPORT_CASE_MISMATCH", "Vaka başka bir hak sahibine ait", "")
	case errors.Is(err, application.ErrReportCaseNotFound):
		problem(w, r, http.StatusNotFound, "health-cases/not-found", "HEALTH_CASE_NOT_FOUND",
			"Sağlık vakası bulunamadı", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A health error carries no clinical data: this module's errors name ids, codes and
		// statuses, and no diagnosis code or note ever reaches one.
		h.logger.Error("health command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
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

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

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

// decodeOptionalJSON accepts an empty body, which the close command allows.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
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

// writeJSON answers with no-store. A clinical body is not something an intermediary should
// keep a copy of, and the answer to the same URL differs by who asked for it.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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

func queryTimestamp(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "RFC3339 biçiminde olmalı"})
		return nil
	}
	return &t
}
