// Package contracthttp serves /api/v1/contracts, /contract-versions, /price-lists and the
// price resolution endpoint. Bodies use the generated contract types; errors are
// problem+json with stable codes (ADR-015). Every money value crosses this boundary as an
// exact decimal string: the generated types map these fields to Go strings, so no float
// exists anywhere in the request or the response path.
package contracthttp

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

	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes (migration 000008). Publishing and retiring take the
// third one and a recent step-up, because a published price sheet is what a claim is paid
// against.
const (
	PermissionRead    = application.PermissionRead
	PermissionManage  = application.PermissionManage
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
	CreateContract        func(http.Handler) http.Handler
	CreateContractVersion func(http.Handler) http.Handler
}

// Handler serves the contract operations.
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

// ContractRoutes mounts everything below /contracts.
func (h *Handler) ContractRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListContracts)
	r.With(wrap(mw.CreateContract)).Post("/", h.CreateContract)
	r.Get("/{contractId}", h.GetContract)
	r.Patch("/{contractId}", h.PatchContract)
	r.Get("/{contractId}/versions", h.ListContractVersions)
	r.With(wrap(mw.CreateContractVersion)).Post("/{contractId}/versions", h.CreateContractVersion)
}

// VersionRoutes mounts everything below /contract-versions. Versions are addressed
// directly rather than under their contract, because a UI reaches one from a version list
// or from a price lookup result rather than by walking down from the agreement.
func (h *Handler) VersionRoutes(r chi.Router) {
	r.Get("/{contractVersionId}", h.GetContractVersion)
	r.Patch("/{contractVersionId}", h.PatchContractVersion)
	r.Post("/{contractVersionId}/submit", h.SubmitContractVersion)
	r.Post("/{contractVersionId}/publish", h.PublishContractVersion)
	r.Post("/{contractVersionId}/retire", h.RetireContractVersion)
	r.Put("/{contractVersionId}/price-lists", h.PutPriceLists)
	r.Get("/{contractVersionId}/package-definitions", h.ListPackageDefinitions)
	r.Put("/{contractVersionId}/package-definitions", h.PutPackageDefinitions)
	r.Get("/{contractVersionId}/provider-quotas", h.ListProviderQuotas)
	r.Put("/{contractVersionId}/provider-quotas", h.PutProviderQuotas)
	r.Get("/{contractVersionId}/payment-term", h.GetPaymentTerm)
	r.Put("/{contractVersionId}/payment-term", h.PutPaymentTerm)
}

// PriceListRoutes mounts everything below /price-lists.
func (h *Handler) PriceListRoutes(r chi.Router) {
	r.Get("/{priceListId}/items", h.ListPriceItems)
	r.Put("/{priceListId}/items", h.PutPriceItems)
}

// PriceRoutes mounts the price resolution endpoint on the tenant router itself: the
// contract path is /api/v1/prices:resolve, a single literal segment rather than a
// sub-resource, so it cannot hang under a chi.Route prefix.
func (h *Handler) PriceRoutes(r chi.Router) {
	r.Post("/prices:resolve", h.ResolvePrice)
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

// allowedToRead reports whether the caller may see this version's price sheet. A published
// or retired version is what everyone downstream is paid against, so contract.read is
// enough; a draft or a version under review is a proposal, and seeing an unagreed price
// takes contract.manage.
// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrContractNotFound):
		problem(w, r, http.StatusNotFound, "contract/contract-not-found", "CONTRACT_NOT_FOUND",
			"Sözleşme bulunamadı", "")
	case errors.Is(err, application.ErrVersionNotFound):
		problem(w, r, http.StatusNotFound, "contract/version-not-found", "CONTRACT_VERSION_NOT_FOUND",
			"Sözleşme sürümü bulunamadı", "")
	case errors.Is(err, application.ErrPriceListNotFound):
		problem(w, r, http.StatusNotFound, "contract/price-list-not-found", "PRICE_LIST_NOT_FOUND",
			"Fiyat listesi bulunamadı", "")
	case errors.Is(err, application.ErrPaymentTermNotFound):
		problem(w, r, http.StatusNotFound, "contract/payment-term-not-found", "PAYMENT_TERM_NOT_FOUND",
			"Bu sürüm için ödeme koşulu tanımlanmamış", "")
	case errors.Is(err, application.ErrPartyNotFound):
		problem(w, r, http.StatusNotFound, "contract/party-not-found", "CONTRACT_PARTY_NOT_FOUND",
			"Sözleşme tarafı bulunamadı", "Ödeyen kurum, sponsor veya sağlayıcı bu kurumda bulunamadı.")
	case errors.Is(err, application.ErrContractCodeTaken):
		problem(w, r, http.StatusConflict, "contract/code-taken", "CONTRACT_CODE_TAKEN",
			"Bu sözleşme kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrPriceListCodeTaken):
		problem(w, r, http.StatusConflict, "contract/price-list-code-taken", "PRICE_LIST_CODE_TAKEN",
			"Bu fiyat listesi kodu bu sürümde kullanılıyor", "")
	case errors.Is(err, application.ErrPackageCodeTaken):
		problem(w, r, http.StatusConflict, "contract/package-code-taken", "PACKAGE_CODE_TAKEN",
			"Bu paket kodu bu sürümde kullanılıyor", "")
	case errors.Is(err, application.ErrQuotaScopeDuplicate):
		problem(w, r, http.StatusConflict, "contract/quota-scope-duplicate", "PROVIDER_QUOTA_DUPLICATE",
			"Aynı kapsam ve dönem için birden fazla kota var", "")
	case errors.Is(err, application.ErrVersionImmutable):
		problem(w, r, http.StatusConflict, "contract/version-immutable", "CONTRACT_VERSION_IMMUTABLE",
			"Yayınlanmış sözleşme sürümü değiştirilemez",
			"Bu sürümden fiyatlanmış teklif ve talepler var; yeni bir sürüm açın.")
	case errors.Is(err, application.ErrVersionOverlap):
		problem(w, r, http.StatusConflict, "contract/version-overlap", "CONTRACT_VERSION_OVERLAP",
			"Bu tarih aralığında yayında başka bir sürüm var",
			"Bir sözleşmenin iki yayınlanmış sürümü aynı günü kapsayamaz.")
	case errors.Is(err, application.ErrVersionTransition):
		problem(w, r, http.StatusConflict, "contract/version-transition-invalid", "CONTRACT_VERSION_TRANSITION_INVALID",
			"Bu durum geçişi yapılamaz", "")
	case errors.Is(err, domain.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "contract/transition-invalid", "CONTRACT_TRANSITION_INVALID",
			"Sözleşme bu duruma geçemez", "Kapatılmış bir sözleşme yeniden açılamaz.")
	case errors.Is(err, application.ErrMakerCheckerSame):
		problem(w, r, http.StatusForbidden, "contract/maker-checker-same-actor", "MAKER_CHECKER_SAME_ACTOR",
			"Yayınlayan, gönderenden farklı olmalı", "Sürümü incelemeye gönderen kullanıcı onu yayınlayamaz.")
	case errors.Is(err, application.ErrCatalogTargetMissing):
		writeValidation(w, r, []domain.FieldError{{
			Field: "items", Code: "NOT_FOUND", Message: "hizmet tanımı, kategori, paket veya lokasyon bulunamadı",
		}})
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		// A contract error never carries personal data: the module stores codes, ids,
		// dates and amounts only.
		h.logger.Error("contract request failed", "error", err)
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

// forbidden is the answer to reading a draft price sheet without contract.manage. It is a
// 403 rather than a 404: the version exists and the caller may know that it does, they
