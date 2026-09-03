package identityhttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// maxAuthBodyBytes caps a credential request body; passwords are limited to 128 runes.
const maxAuthBodyBytes = 4 << 10

// Handler serves /api/v1/session.
type Handler struct {
	svc     *application.Service
	cookies CookieConfig
	signing []byte
	logger  *slog.Logger
}

// NewHandler wires the session endpoints.
func NewHandler(svc *application.Service, cookies CookieConfig, signingKey []byte, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, cookies: cookies, signing: signingKey, logger: logger}
}

// Routes mounts the session endpoints on r. The caller applies the session middleware and
// the rate limiter around them.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.GetSession)
	r.Post("/login", h.Login)
	r.Post("/logout", h.Logout)
	r.Post("/step-up", h.StepUp)
	r.Post("/password", h.ChangePassword)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	ActorID            string  `json:"actorId"`
	DisplayName        string  `json:"displayName,omitempty"`
	ActiveTenantID     *string `json:"activeTenantId"`
	CSRFToken          string  `json:"csrfToken"`
	ExpiresAt          string  `json:"expiresAt"`
	StepUpExpiresAt    *string `json:"stepUpExpiresAt"`
	MustChangePassword bool    `json:"mustChangePassword"`
}

// Login authenticates a user name and password and starts a session.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	// An existing session is replaced, not reused: logging in again must never widen an
	// old session's lifetime or keep its step-up window.
	if old := h.cookies.read(r); old != "" {
		_ = h.svc.Logout(r.Context(), old)
	}

	result, err := h.svc.Login(r.Context(), application.LoginInput{
		Username: in.Username,
		Password: in.Password,
		Meta: application.SessionMeta{
			UserAgentHash: userAgentHash(r),
			SourceIP:      clientAddr(r),
		},
	})
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	h.cookies.set(w, result.Session.ID)
	writeJSON(w, http.StatusOK, h.sessionBody(result))
}

// GetSession returns the current session and the CSRF token the frontend must echo.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		h.unauthenticated(w, r)
		return
	}
	view, err := h.svc.Describe(r.Context(), session.ID)
	if err != nil {
		h.cookies.clear(w)
		h.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.sessionBody(view))
}

// Logout ends the session and clears the cookie. It is idempotent.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if id := h.cookies.read(r); id != "" {
		if err := h.svc.Logout(r.Context(), id); err != nil && !errors.Is(err, identity.ErrUnauthenticated) {
			h.logger.Error("logout failed", "error", err)
		}
	}
	h.cookies.clear(w)
	w.WriteHeader(http.StatusNoContent)
}

type stepUpRequest struct {
	Password string `json:"password"`
}

// StepUp re-verifies the password and opens the step-up window.
func (h *Handler) StepUp(w http.ResponseWriter, r *http.Request) {
	var in stepUpRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		h.unauthenticated(w, r)
		return
	}
	until, err := h.svc.StepUp(r.Context(), session.ID, in.Password)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stepUpExpiresAt": until.UTC().Format(time.RFC3339)})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword replaces the caller's own password and ends their other sessions.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var in changePasswordRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		h.unauthenticated(w, r)
		return
	}
	if err := h.svc.ChangePassword(r.Context(), session.ID, in.CurrentPassword, in.NewPassword); err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	// The session id is unchanged, so the cookie stays valid.
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) sessionBody(v application.SessionView) sessionResponse {
	s := v.Session
	body := sessionResponse{
		ActorID:            s.ActorID.String(),
		DisplayName:        v.DisplayName,
		CSRFToken:          domain.DeriveCSRFToken(h.signing, s.ID),
		ExpiresAt:          s.ExpiresAt.UTC().Format(time.RFC3339),
		MustChangePassword: v.MustChangePassword,
	}
	if s.ActiveTenantID.Valid {
		id := s.ActiveTenantID.UUID.String()
		body.ActiveTenantID = &id
	}
	if !s.StepUpUntil.IsZero() {
		until := s.StepUpUntil.UTC().Format(time.RFC3339)
		body.StepUpExpiresAt = &until
	}
	return body
}

// writeAuthError maps application errors to stable codes. Login failures never say
// whether the user name existed.
func (h *Handler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidCredentials):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "identity/invalid-credentials",
			Title:  "Kullanıcı adı veya parola hatalı",
			Status: http.StatusUnauthorized,
			Code:   "INVALID_CREDENTIALS",
		})
	case errors.Is(err, application.ErrAccountLocked):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "identity/account-locked",
			Title:  "Hesap geçici olarak kilitlendi",
			Status: http.StatusForbidden,
			Code:   "ACCOUNT_LOCKED",
			Detail: "Art arda hatalı deneme nedeniyle hesap kısa süreliğine kilitlendi.",
		})
	case errors.Is(err, application.ErrActorSuspended):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "identity/actor-suspended",
			Title:  "Hesap kullanıma kapalı",
			Status: http.StatusForbidden,
			Code:   "ACTOR_SUSPENDED",
		})
	case errors.Is(err, identity.ErrSessionNotFound), errors.Is(err, identity.ErrUnauthenticated):
		h.unauthenticated(w, r)
	case errors.Is(err, domain.ErrPasswordTooShort), errors.Is(err, domain.ErrPasswordTooLong),
		errors.Is(err, domain.ErrPasswordTooCommon):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "identity/password-policy",
			Title:  "Parola kurallara uymuyor",
			Status: http.StatusUnprocessableEntity,
			Code:   "PASSWORD_POLICY_VIOLATION",
			Detail: err.Error(),
			Errors: []httpx.FieldError{{Field: "newPassword", Code: "PASSWORD_POLICY_VIOLATION"}},
		})
	default:
		h.logger.Error("authentication failed", "error", err)
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "generic/internal-error",
			Title:  "Beklenmeyen hata",
			Status: http.StatusInternalServerError,
			Code:   "INTERNAL_ERROR",
		})
	}
}

func (h *Handler) unauthenticated(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   httpx.ProblemTypeBase + "identity/unauthenticated",
		Title:  "Oturum bulunamadı",
		Status: http.StatusUnauthorized,
		Code:   "UNAUTHENTICATED",
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "generic/invalid-request-body",
			Title:  "İstek gövdesi geçersiz",
			Status: http.StatusBadRequest,
			Code:   "INVALID_REQUEST_BODY",
		})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func userAgentHash(r *http.Request) []byte {
	meta := audit.RequestMetaFrom(r.Context())
	return meta.UserAgentHash
}

func clientAddr(r *http.Request) netip.Addr {
	return audit.RequestMetaFrom(r.Context()).SourceIP
}
