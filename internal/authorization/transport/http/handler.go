// Package authorizationhttp serves /api/v1/authorizations, /api/v1/fulfilments and the
// voucher redemption endpoint. Bodies use the generated contract types; errors are
// problem+json with stable codes (ADR-015).
//
// Two things shape the routes here. Every command that moves entitlement is a route of
// its own with its own permission — creating a promise, delivering against it, spending
// it — because "who may do this" is then a routing fact rather than a branch in a body.
// And the voucher token is only ever read from a request body: a URL is written to access
// logs, browser history and referrers, and a voucher in any of them is a voucher somebody
// else can spend.
package authorizationhttp

import (
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

	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/authorization/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes (migration 000026). Promising entitlement, delivering
// against a promise and spending a voucher are three separate grants on purpose: the
// person at the counter is not the person who decides what a member is owed.
const (
	PermissionManage = application.PermissionManage
	PermissionRecord = application.PermissionRecord
	PermissionRedeem = application.PermissionRedeem
)

// readPermissions are the grants that may read an authorization or a fulfilment. Reading
// is not a permission of its own: anybody who may promise, deliver or read the request
// behind it may see what was promised, and inventing a fourth grant would only mean a
// role that can create something it cannot then look at.
var readPermissions = []string{
	application.PermissionManage, application.PermissionRecord, application.PermissionRequestRead,
}

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
//
// issueVoucher has no field here, and that is the point. The Idempotency-Key middleware
// stores the response body in system.idempotency_record so a replay can be answered from
// it, and the issue response is the one place a voucher's plaintext ever exists. Wrapping
// that route would write the token into a column — the one thing this package must never
// do — so the route cannot be wrapped at all rather than merely being left unwrapped by a
// wiring that somebody could later "fix".
type Middlewares struct {
	CreateAuthorization func(http.Handler) http.Handler
	ExtendAuthorization func(http.Handler) http.Handler
	CancelAuthorization func(http.Handler) http.Handler
	CreateFulfilment    func(http.Handler) http.Handler
	CompleteFulfilment  func(http.Handler) http.Handler
	CancelFulfilment    func(http.Handler) http.Handler
	RedeemVoucher       func(http.Handler) http.Handler
}

// Handler serves the authorization, fulfilment and voucher operations.
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

// AuthorizationRoutes mounts everything below /authorizations.
func (h *Handler) AuthorizationRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListAuthorizations)
	r.With(wrap(mw.CreateAuthorization)).Post("/", h.CreateAuthorization)
	r.Get("/{authorizationId}", h.GetAuthorization)
	r.With(wrap(mw.ExtendAuthorization)).Post("/{authorizationId}/extend", h.ExtendAuthorization)
	r.With(wrap(mw.CancelAuthorization)).Post("/{authorizationId}/cancel", h.CancelAuthorization)
	// Deliberately unwrapped: see Middlewares. Issuing twice is two vouchers, which costs
	// nothing — a voucher moves no entitlement until it is redeemed, and redemption is
	// guarded by the voucher's own status.
	r.Post("/{authorizationId}/vouchers", h.IssueVoucher)
}

// FulfilmentRoutes mounts everything below /fulfilments.
func (h *Handler) FulfilmentRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListFulfilments)
	r.With(wrap(mw.CreateFulfilment)).Post("/", h.CreateFulfilment)
	r.Get("/{fulfilmentId}", h.GetFulfilment)
	r.With(wrap(mw.CompleteFulfilment)).Post("/{fulfilmentId}/complete", h.CompleteFulfilment)
	r.With(wrap(mw.CancelFulfilment)).Post("/{fulfilmentId}/cancel", h.CancelFulfilment)
}

// VoucherRoutes mounts the redemption endpoint on the tenant router itself: the contract
// path is /api/v1/vouchers:redeem, a single literal segment rather than a sub-resource,
// so it cannot hang under a chi.Route prefix.
func (h *Handler) VoucherRoutes(r chi.Router, mw Middlewares) {
	r.With(wrap(mw.RedeemVoucher)).Post("/vouchers:redeem", h.RedeemVoucher)
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
// The denial is audited against the first of them, which is the one the endpoint is
// really about; a caller holding none of them is refused exactly as if only that one
// existed.
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

// writeError maps application, domain and ledger errors to the problem codes of the work
// package. The ledger's own refusals keep their own codes: "there is not enough left" and
// "this account is frozen" are different facts and a caller can act on only one of them.
//
//nolint:funlen // one error is one line; splitting the table hides which code is missing
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrAuthorizationNotFound):
		problem(w, r, http.StatusNotFound, "authorizations/not-found", "AUTHORIZATION_NOT_FOUND",
			"Ön onay bulunamadı", "")
	case errors.Is(err, application.ErrFulfilmentNotFound):
		problem(w, r, http.StatusNotFound, "fulfilments/not-found", "FULFILMENT_NOT_FOUND",
			"Hizmet kaydı bulunamadı", "")
	case errors.Is(err, application.ErrVoucherNotFound):
		problem(w, r, http.StatusNotFound, "vouchers/not-found", "VOUCHER_NOT_FOUND",
			"Kupon bulunamadı", "")
	case errors.Is(err, application.ErrRequestNotFound):
		problem(w, r, http.StatusNotFound, "service-requests/not-found", "SERVICE_REQUEST_NOT_FOUND",
			"Hizmet talebi bulunamadı", "")
	case errors.Is(err, application.ErrRequestNotApproved):
		problem(w, r, http.StatusConflict, "authorizations/request-not-approved", "REQUEST_NOT_APPROVED",
			"Talep onaylanmadan ön onay verilemez",
			"Ön onay yalnızca APPROVED veya PARTIALLY_APPROVED bir talep için oluşturulabilir.")
	case errors.Is(err, application.ErrNoApprovedItems):
		problem(w, r, http.StatusUnprocessableEntity, "authorizations/no-approved-items",
			"AUTHORIZATION_NO_APPROVED_ITEMS", "Talepte onaylanmış kalem yok", "")
	case errors.Is(err, application.ErrAccountNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "authorizations/entitlement-account-missing",
			"ENTITLEMENT_ACCOUNT_NOT_FOUND", "Bu kalem için açık bir hak hesabı yok",
			"Hizmetin kodu ile eşleşen bir hak tanımı bulunamadı.")
	case errors.Is(err, ledger.ErrInsufficient):
		problem(w, r, http.StatusConflict, "authorizations/balance-insufficient", "BALANCE_INSUFFICIENT",
			"Hak bakiyesi yetersiz", "Ön onay verilemedi; hiçbir kalem için hak ayrılmadı.")
	case errors.Is(err, ledger.ErrAccountFrozen):
		problem(w, r, http.StatusConflict, "authorizations/account-frozen", "ENTITLEMENT_ACCOUNT_FROZEN",
			"Hak hesabı donduruldu", "")
	case errors.Is(err, ledger.ErrAccountClosed):
		problem(w, r, http.StatusConflict, "authorizations/account-closed", "ENTITLEMENT_ACCOUNT_CLOSED",
			"Hak hesabı kapalı", "")
	case errors.Is(err, ledger.ErrReservationClosed):
		problem(w, r, http.StatusConflict, "authorizations/reservation-closed", "ENTITLEMENT_RESERVATION_CLOSED",
			"Bu ön onayın hak rezervasyonu kapanmış", "")
	case errors.Is(err, application.ErrAuthorizationNotActive):
		problem(w, r, http.StatusConflict, "authorizations/not-active", "AUTHORIZATION_NOT_ACTIVE",
			"Ön onay artık hak tutmuyor",
			"Kullanılmış, süresi geçmiş veya iptal edilmiş bir ön onay üzerinde işlem yapılamaz.")
	case errors.Is(err, application.ErrOverFulfilment):
		problem(w, r, http.StatusUnprocessableEntity, "fulfilments/over-fulfilment", "OVER_FULFILMENT",
			"Onaylanandan fazlası verilemez", "")
	case errors.Is(err, application.ErrItemNotInAuthorization):
		problem(w, r, http.StatusUnprocessableEntity, "fulfilments/item-unknown",
			"AUTHORIZATION_ITEM_UNKNOWN", "Kalem bu ön onaya ait değil", "")
	case errors.Is(err, application.ErrFulfilmentCompleted):
		problem(w, r, http.StatusConflict, "fulfilments/already-completed", "FULFILMENT_ALREADY_COMPLETED",
			"Tamamlanmış hizmet kaydı iptal edilemez",
			"Tamamlanan kayıt haktan düşmüştür; düzeltme ters kayıtla yapılır.")
	case errors.Is(err, application.ErrFulfilmentNotRecorded):
		problem(w, r, http.StatusConflict, "fulfilments/not-recorded", "FULFILMENT_NOT_RECORDED",
			"Bu hizmet kaydı üzerinde işlem yapılamaz", "")
	case errors.Is(err, application.ErrVoucherAlreadyIssued):
		problem(w, r, http.StatusConflict, "vouchers/already-issued", "VOUCHER_ALREADY_ISSUED",
			"Bu ön onay için kullanılabilir bir kupon zaten var",
			"Kupon kodu yalnızca üretildiği anda gösterilir ve saklanmaz. Yeni kupon için "+
				"mevcut kuponun kullanılmasını, süresinin dolmasını veya iptal edilmesini bekleyin.")
	case errors.Is(err, application.ErrVoucherAlreadyRedeemed):
		problem(w, r, http.StatusConflict, "vouchers/already-redeemed", "VOUCHER_ALREADY_REDEEMED",
			"Kupon zaten kullanılmış", "")
	case errors.Is(err, application.ErrVoucherExpired):
		problem(w, r, http.StatusConflict, "vouchers/expired", "VOUCHER_EXPIRED",
			"Kupon geçerlilik süresi dışında", "")
	case errors.Is(err, application.ErrVoucherRevoked):
		problem(w, r, http.StatusConflict, "vouchers/revoked", "VOUCHER_REVOKED",
			"Kupon iptal edilmiş", "")
	case errors.Is(err, application.ErrIdempotencyKeyRequired):
		problem(w, r, http.StatusBadRequest, "generic/idempotency-key-required", "IDEMPOTENCY_KEY_REQUIRED",
			"Idempotency-Key başlığı gerekli", "")
	case errors.Is(err, ledger.ErrIdempotencyKeyReuse):
		problem(w, r, http.StatusConflict, "generic/idempotency-key-reused", "IDEMPOTENCY_KEY_REUSED",
			"Aynı anahtar farklı bir istekle kullanıldı", "")
	case errors.Is(err, application.ErrReferenceCollision):
		problem(w, r, http.StatusConflict, "authorizations/reference-unavailable",
			"AUTHORIZATION_REFERENCE_UNAVAILABLE", "Numara üretilemedi", "Lütfen isteği yeniden gönderin.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// An authorization error carries no personal data: this module stores ids, codes,
		// timestamps and quantities only, and a voucher token never reaches an error at
		// all — the lookup argument is its digest.
		h.logger.Error("authorization command failed", "error", err)
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

// idempotencyKey is the header the create commands make their replay contract out of. The
// middleware in cmd/api answers a repeated call from its own record; the service uses the
// same key to make sure a replay that reaches it takes no second set of reservations.
func idempotencyKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
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

// decodeOptionalJSON accepts an empty body, which issueVoucher allows.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
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

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := t.UTC()
	return &value
}
