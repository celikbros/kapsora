// Package notificationhttp serves /api/v1/notification-templates,
// /api/v1/notification-messages and /api/v1/notification-preferences. Bodies use the
// generated contract types; errors are problem+json with stable codes (ADR-015).
//
// Two things shape the routes here. Writing the message everybody gets and reading what
// one person was actually sent are different permissions — notification.manage and
// notification.read — because the first is configuration and the second is a record of
// people's dealings with the payer. And nothing here sends anything: publishing a template
// and resending a message both write a row and an outbox event, and the worker is what
// talks to a mail server. An HTTP request must never be able to wait on a relay.
package notificationhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. notification.manage is in the catalogue from migration
// 000008; notification.read is added by migration 000029.
const (
	PermissionManage = application.PermissionManage
	PermissionRead   = application.PermissionRead
)

// readPermissions are the grants that may read templates, messages and preferences.
// Anybody who may write the templates may read what they produced; inventing a rule where
// they could not would only mean an operator who can publish a message and not see whether
// it arrived.
var readPermissions = []string{application.PermissionRead, application.PermissionManage}

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateTemplate  func(http.Handler) http.Handler
	PublishTemplate func(http.Handler) http.Handler
	ResendMessage   func(http.Handler) http.Handler
	PutPreferences  func(http.Handler) http.Handler
}

// Handler serves the template, message and preference operations.
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

// TemplateRoutes mounts everything below /notification-templates.
func (h *Handler) TemplateRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListNotificationTemplates)
	r.With(wrap(mw.CreateTemplate)).Post("/", h.CreateNotificationTemplate)
	r.Get("/{templateId}", h.GetNotificationTemplate)
	r.With(wrap(mw.PublishTemplate)).Post("/{templateId}/publish", h.PublishNotificationTemplate)
}

// MessageRoutes mounts everything below /notification-messages.
func (h *Handler) MessageRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListNotificationMessages)
	r.Get("/{messageId}", h.GetNotificationMessage)
	r.With(wrap(mw.ResendMessage)).Post("/{messageId}/resend", h.ResendNotificationMessage)
}

// PreferenceRoutes mounts everything below /notification-preferences.
func (h *Handler) PreferenceRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.GetNotificationPreferences)
	r.With(wrap(mw.PutPreferences)).Put("/", h.PutNotificationPreferences)
}

func wrap(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mw == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return mw
}

// requireManage resolves the request context of a caller who may change something, or
// writes the denial through the auditing denier. Every write in this package needs the
// same grant, so the permission is named here rather than at each call: a route that
// accepted a different one would be a route where "who may do this" stopped being one
// answer.
func (h *Handler) requireManage(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), PermissionManage)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionManage)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// requireAny resolves the request context for a caller holding any of the permissions. The
// denial is audited against the first of them, which is the one the endpoint is really
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

// writeError maps application and domain errors to the problem codes of this package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrTemplateNotFound):
		problem(w, r, http.StatusNotFound, "notification-templates/not-found",
			"NOTIFICATION_TEMPLATE_NOT_FOUND", "Bildirim şablonu bulunamadı", "")
	case errors.Is(err, application.ErrMessageNotFound):
		problem(w, r, http.StatusNotFound, "notification-messages/not-found",
			"NOTIFICATION_MESSAGE_NOT_FOUND", "Bildirim kaydı bulunamadı", "")
	case errors.Is(err, application.ErrTemplateNotDraft):
		problem(w, r, http.StatusConflict, "notification-templates/not-draft",
			"NOTIFICATION_TEMPLATE_NOT_DRAFT", "Yalnızca taslak şablon yayınlanabilir",
			"Yayınlanmış bir şablon değiştirilemez; yeni bir sürüm oluşturun.")
	case errors.Is(err, application.ErrTemplateImmutable):
		problem(w, r, http.StatusConflict, "notification-templates/immutable",
			"NOTIFICATION_TEMPLATE_IMMUTABLE", "Yayınlanmış şablon değiştirilemez",
			"Değişiklik için yeni bir sürüm oluşturup yayınlayın.")
	case errors.Is(err, application.ErrTemplateVersionExists):
		problem(w, r, http.StatusConflict, "notification-templates/version-exists",
			"NOTIFICATION_TEMPLATE_VERSION_EXISTS", "Bu şablon sürümü zaten var", "")
	case errors.Is(err, application.ErrNoPublishedTemplate):
		problem(w, r, http.StatusConflict, "notification-templates/none-published",
			"NOTIFICATION_TEMPLATE_MISSING", "Bu olay için yayınlanmış şablon yok",
			"Önce olaya, kanala ve dile uygun bir şablon yayınlayın.")
	case errors.Is(err, application.ErrMessageNotResendable):
		problem(w, r, http.StatusConflict, "notification-messages/not-resendable",
			"NOTIFICATION_MESSAGE_NOT_RESENDABLE", "Bu bildirim yeniden gönderilemez",
			"Gövdesi üretilmemiş ya da hâlâ gönderim sırasında olan bir bildirim tekrarlanamaz.")
	case errors.Is(err, application.ErrRecipientOptedOut):
		problem(w, r, http.StatusConflict, "notification-messages/opted-out",
			"NOTIFICATION_RECIPIENT_OPTED_OUT", "Alıcı bu bildirimi kapatmış",
			"Kişinin tercihi yönetici işlemiyle geçersiz kılınamaz.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A notification error carries no personal data: this module stores ids, codes,
		// safe variables and statuses, and a rendered body never reaches an error at all.
		h.logger.Error("notification command failed", "error", err)
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
