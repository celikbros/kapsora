package identityhttp

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// TenantHeader names the tenant a tenant-scoped request acts in. It must equal the
// session's active tenant; it is never trusted on its own.
const TenantHeader = "X-Tenant-ID"

// WithAuthorizer enables RequireTenantContext and audited denials on the middleware.
func (m *Middleware) WithAuthorizer(a *application.Authorizer) *Middleware {
	m.authz = a
	return m
}

// RequireTenantContext resolves identity.RequestContext for tenant-scoped routes. Without
// a session it answers 401; without a valid, matching X-Tenant-ID it answers 400 or 403.
// It never reveals whether the requested tenant exists.
func (m *Middleware) RequireTenantContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := identity.SessionFromContext(r.Context())
		if !ok {
			WriteAuthError(w, r, identity.ErrUnauthenticated, m.logger)
			return
		}
		raw := r.Header.Get(TenantHeader)
		tenantID, err := uuid.Parse(raw)
		if raw == "" || err != nil {
			httpx.WriteProblem(w, r, httpx.Problem{
				Type:   httpx.ProblemTypeBase + "identity/tenant-header-required",
				Title:  "Tenant başlığı gerekli",
				Status: http.StatusBadRequest,
				Code:   "TENANT_HEADER_REQUIRED",
				Detail: "X-Tenant-ID başlığı geçerli bir UUID olmalıdır.",
			})
			return
		}
		rc, err := m.authz.ResolveTenantContext(r.Context(), session, tenantID, httpx.RequestIDFrom(r.Context()))
		if err != nil {
			WriteAuthError(w, r, err, m.logger)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
	})
}

// Deny writes the problem for a failed identity.Require / RequireStepUp and audits it
// (SECURITY, DENIED). Handlers call it instead of WriteAuthError for permission checks so
// every denial leaves a trace (v1.2 section 21.1).
func (m *Middleware) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	if m.authz != nil {
		if rc, ok := identity.FromContext(r.Context()); ok {
			reason := "PERMISSION_DENIED"
			if errors.Is(err, identity.ErrStepUpRequired) {
				reason = "STEP_UP_REQUIRED"
			}
			m.authz.RecordDenied(r.Context(), rc, permission, reason)
		}
	}
	WriteAuthError(w, r, err, m.logger)
}

// WriteAuthError maps identity and authentication errors to problem+json. Other modules
// reuse it for the errors of identity.Require; it never leaks resource existence.
func WriteAuthError(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	switch {
	case errors.Is(err, identity.ErrUnauthenticated), errors.Is(err, identity.ErrSessionNotFound):
		problem(w, r, http.StatusUnauthorized, "identity/unauthenticated", "UNAUTHENTICATED", "Oturum bulunamadı", "")
	case errors.Is(err, identity.ErrPermissionDenied):
		problem(w, r, http.StatusForbidden, "identity/permission-denied", "PERMISSION_DENIED", "Bu işlem için yetkiniz yok", "")
	case errors.Is(err, identity.ErrStepUpRequired):
		problem(w, r, http.StatusForbidden, "identity/step-up-required", "STEP_UP_REQUIRED", "Bu işlem için parolanızı yeniden doğrulayın", "")
	case errors.Is(err, application.ErrTenantMismatch):
		problem(w, r, http.StatusForbidden, "identity/tenant-mismatch", "TENANT_MISMATCH", "Seçili kurum ile istek uyuşmuyor", "Önce kurumu değiştirin (switch-tenant).")
	case errors.Is(err, application.ErrNoMembership):
		problem(w, r, http.StatusForbidden, "identity/tenant-access-denied", "TENANT_ACCESS_DENIED", "Bu kuruma erişiminiz yok", "")
	case errors.Is(err, application.ErrInvalidCredentials):
		problem(w, r, http.StatusUnauthorized, "identity/invalid-credentials", "INVALID_CREDENTIALS", "Kullanıcı adı veya parola hatalı", "")
	case errors.Is(err, application.ErrAccountLocked):
		problem(w, r, http.StatusForbidden, "identity/account-locked", "ACCOUNT_LOCKED", "Hesap geçici olarak kilitlendi", "Art arda hatalı deneme nedeniyle hesap kısa süreliğine kilitlendi.")
	case errors.Is(err, application.ErrActorSuspended):
		problem(w, r, http.StatusForbidden, "identity/actor-suspended", "ACTOR_SUSPENDED", "Hesap kullanıma kapalı", "")
	case errors.Is(err, application.ErrTenantCodeExists):
		problem(w, r, http.StatusConflict, "identity/tenant-code-exists", "TENANT_CODE_EXISTS", "Bu kurum kodu zaten kullanılıyor", "")
	case errors.Is(err, domain.ErrPasswordTooShort), errors.Is(err, domain.ErrPasswordTooLong), errors.Is(err, domain.ErrPasswordTooCommon):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "identity/password-policy",
			Title:  "Parola kurallara uymuyor",
			Status: http.StatusUnprocessableEntity,
			Code:   "PASSWORD_POLICY_VIOLATION",
			Detail: err.Error(),
			Errors: []httpx.FieldError{{Field: "newPassword", Code: "PASSWORD_POLICY_VIOLATION"}},
		})
	default:
		logger.Error("authentication or authorization failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}

func problem(w http.ResponseWriter, r *http.Request, status int, typ, code, title, detail string) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   httpx.ProblemTypeBase + typ,
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
	})
}
