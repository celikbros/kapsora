package identityhttp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type InvitationHandler struct {
	svc        *application.InvitationService
	middleware *Middleware
	cursors    *httpx.CursorCodec
	logger     *slog.Logger
}

func NewInvitationHandler(svc *application.InvitationService, middleware *Middleware, cursors *httpx.CursorCodec, logger *slog.Logger) *InvitationHandler {
	return &InvitationHandler{svc: svc, middleware: middleware, cursors: cursors, logger: logger}
}

func (h *InvitationHandler) ManagerRoutes(r chi.Router, cancelMiddleware func(http.Handler) http.Handler) {
	r.Use(invitationNoStore)
	r.Get("/", h.List)
	r.With(h.manageAuthorization).Post("/", h.Create)
	r.Get("/{invitationId}", h.Get)
	r.With(h.manageAuthorization, cancelMiddleware).Post("/{invitationId}/cancel", h.Cancel)
}

func (h *InvitationHandler) RecipientRoutes(r chi.Router) {
	r.Use(invitationNoStore)
	r.Post("/inspect", h.Inspect)
	r.Post("/accept-existing", h.AcceptExisting)
}

func invitationNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *InvitationHandler) manageAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := identity.RequireStepUp(r.Context(), "identity.user.manage")
		if err == nil {
			err = h.svc.AuthorizeManage(r.Context(), rc)
		}
		if err != nil {
			if errors.Is(err, identity.ErrPermissionDenied) || errors.Is(err, identity.ErrStepUpRequired) || errors.Is(err, identity.ErrUnauthenticated) {
				h.middleware.Deny(w, r, err, "identity.user.manage")
			} else {
				h.internal(w, r, err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *InvitationHandler) requireRead(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), "identity.user.read")
	if err != nil {
		h.middleware.Deny(w, r, err, "identity.user.read")
		return rc, false
	}
	return rc, true
}

func invitationBody(item application.TenantInvitation) any {
	return struct {
		InvitationID    string `json:"invitationId"`
		MaskedRecipient string `json:"maskedRecipient"`
		Status          string `json:"status"`
		CreatedAt       string `json:"createdAt"`
		ExpiresAt       string `json:"expiresAt"`
		RowVersion      int64  `json:"rowVersion"`
		DeliveryStatus  string `json:"deliveryStatus"`
	}{item.ID.String(), item.MaskedRecipient, item.Status, item.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), item.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"), item.RowVersion, item.DeliveryStatus}
}

func (h *InvitationHandler) List(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireRead(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "PENDING", "ACCEPTED", "CANCELLED", "EXPIRED":
	default:
		h.badQuery(w, r)
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			h.badQuery(w, r)
			return
		}
	}
	filter := application.InvitationFilter{Status: status, Limit: httpx.ClampLimit(limit) + 1}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		decoded, _, err := h.cursors.Decode(cursor)
		if err != nil {
			h.badQuery(w, r)
			return
		}
		filter.AfterAt = &decoded.CreatedAt
		filter.AfterID = decoded.ID
	}
	items, err := h.svc.List(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	pageSize := filter.Limit - 1
	var next *string
	if len(items) > pageSize {
		items = items[:pageSize]
		last := items[len(items)-1]
		encoded := h.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		next = &encoded
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		result = append(result, invitationBody(item))
	}
	writeJSON(w, struct {
		Items      []any   `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}{result, next})
}

func (h *InvitationHandler) Get(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireRead(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "invitationId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	item, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(item.RowVersion))
	writeJSON(w, invitationBody(item))
}

func invitationKey(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	return key, len(key) >= 16 && len(key) <= 128
}

func strictInvitationJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
		return false
	}
	return true
}

func (h *InvitationHandler) Create(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), "identity.user.manage")
	if err != nil {
		h.middleware.Deny(w, r, err, "identity.user.manage")
		return
	}
	key, valid := invitationKey(r)
	if !valid {
		problem(w, r, http.StatusBadRequest, "generic/idempotency-key-required", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key gerekli", "")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !strictInvitationJSON(w, r, &body) {
		return
	}
	item, err := h.svc.Create(r.Context(), rc, body.Email, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(item.RowVersion))
	writeJSON(w, invitationBody(item))
}

func (h *InvitationHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), "identity.user.manage")
	if err != nil {
		h.middleware.Deny(w, r, err, "identity.user.manage")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "invitationId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	version, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 64)
	if err != nil || version < 1 || raw != directoryETag(version) {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED", "If-Match gerekli", "")
		return
	}
	// Cancellation takes an empty body. Do not let an unrecognized JSON payload enter
	// the replay scope as a second, undocumented command input.
	content, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(content) != 0 {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Boş gövde gerekli", "")
		return
	}
	item, err := h.svc.Cancel(r.Context(), rc, id, version)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(item.RowVersion))
	writeJSON(w, invitationBody(item))
}

func (h *InvitationHandler) recipientActor(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	session, ok := identity.SessionFromContext(r.Context())
	if !ok || session.ActorID == uuid.Nil {
		WriteAuthError(w, r, identity.ErrUnauthenticated, h.logger)
		return uuid.Nil, false
	}
	return session.ActorID, true
}

func (h *InvitationHandler) Inspect(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.recipientActor(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !strictInvitationJSON(w, r, &body) {
		return
	}
	result, err := h.svc.Inspect(r.Context(), actorID, body.Code)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, struct {
		TenantDisplayName string `json:"tenantDisplayName"`
		InvitationStatus  string `json:"invitationStatus"`
		ExpiresAt         string `json:"expiresAt"`
	}{
		result.TenantDisplayName, result.Status, result.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")})
}

func (h *InvitationHandler) AcceptExisting(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.recipientActor(w, r)
	if !ok {
		return
	}
	key, valid := invitationKey(r)
	if !valid {
		problem(w, r, http.StatusBadRequest, "generic/idempotency-key-required", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key gerekli", "")
		return
	}
	var body struct {
		Code      string `json:"code"`
		Confirmed bool   `json:"confirmed"`
	}
	if !strictInvitationJSON(w, r, &body) {
		return
	}
	if !body.Confirmed {
		problem(w, r, http.StatusBadRequest, "identity/invitation-confirmation-required", "INVITATION_CONFIRMATION_REQUIRED", "Katılım onayı gerekli", "")
		return
	}
	result, err := h.svc.AcceptExisting(r.Context(), actorID, body.Code, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, struct {
		TenantID          string `json:"tenantId"`
		TenantDisplayName string `json:"tenantDisplayName"`
		MembershipID      string `json:"membershipId"`
		MembershipStatus  string `json:"membershipStatus"`
		AccessPending     bool   `json:"accessPending"`
	}{
		result.TenantID.String(), result.TenantDisplayName, result.MembershipID.String(), "ACTIVE", result.AccessPending})
}

func (h *InvitationHandler) badQuery(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusBadRequest, "identity/invitation-query-invalid", "INVITATION_QUERY_INVALID", "Geçersiz liste isteği", "")
}
func (h *InvitationHandler) notFound(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusNotFound, "identity/invitation-not-found", "INVITATION_NOT_FOUND", "Davet bulunamadı", "")
}
func (h *InvitationHandler) internal(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("identity invitation failed", "error", err)
	problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
}
func (h *InvitationHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied), errors.Is(err, identity.ErrStepUpRequired), errors.Is(err, identity.ErrUnauthenticated):
		h.middleware.Deny(w, r, err, "identity.user.manage")
	case errors.Is(err, application.ErrInvitationUnavailable):
		problem(w, r, http.StatusNotFound, "identity/invitation-unavailable", "INVITATION_UNAVAILABLE", "Davet kullanılamıyor", "")
	case errors.Is(err, application.ErrInvitationNotFound):
		h.notFound(w, r)
	case errors.Is(err, application.ErrInvitationInvalidEmail):
		problem(w, r, http.StatusBadRequest, "identity/invitation-email-invalid", "INVITATION_EMAIL_INVALID", "Geçersiz e-posta", "")
	case errors.Is(err, application.ErrInvitationDeliveryDisabled):
		problem(w, r, http.StatusServiceUnavailable, "identity/invitation-delivery-disabled", "INVITATION_DELIVERY_DISABLED", "Davet gönderimi etkin değil", "")
	case errors.Is(err, application.ErrInvitationPendingExists):
		problem(w, r, http.StatusConflict, "identity/invitation-pending-exists", "INVITATION_PENDING_EXISTS", "Bu alıcı için bekleyen davet var", "")
	case errors.Is(err, application.ErrInvitationStateConflict):
		problem(w, r, http.StatusConflict, "identity/invitation-state-conflict", "INVITATION_STATE_CONFLICT", "Davet artık beklemiyor", "")
	case errors.Is(err, application.ErrInvitationMembershipConflict):
		problem(w, r, http.StatusConflict, "identity/invitation-membership-conflict", "INVITATION_MEMBERSHIP_CONFLICT", "Mevcut üyelik yeniden etkinleştirilemez", "")
	case errors.Is(err, application.ErrInvitationVersionConflict):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH", "Davet değişti", "")
	case errors.Is(err, application.ErrInvitationKeyReused):
		problem(w, r, http.StatusConflict, "generic/idempotency-key-reused", "IDEMPOTENCY_KEY_REUSED", "Komut anahtarı değişen istekle kullanıldı", "")
	default:
		h.internal(w, r, err)
	}
}
