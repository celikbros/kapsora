// Package organizationhttp serves /api/v1/organizations. Bodies use the generated contract
// types; errors are problem+json with stable codes (ADR-015).
package organizationhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/organization/application"
	"github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes.
const (
	PermissionRead   = "organization.read"
	PermissionManage = "organization.manage"
)

const maxBodyBytes = 64 << 10

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Handler serves the four organization operations.
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

// Routes mounts the operations; the caller wraps Create with the idempotency middleware.
func (h *Handler) Routes(r chi.Router, create func(http.Handler) http.Handler) {
	r.Get("/", h.List)
	r.With(create).Post("/", h.Create)
	r.Get("/{organizationId}", h.Get)
	r.Patch("/{organizationId}", h.Update)
}

// Create implements createTenantOrganization.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionManage)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionManage)
		return
	}
	var body kapsorav1.CreateOrganizationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewOrganization{
		LegalName: body.LegalName, DisplayName: body.DisplayName,
		OrganizationKind: string(body.OrganizationKind), RelationshipRole: string(body.RelationshipRole),
	}
	if body.CountryCode != nil {
		in.CountryCode = *body.CountryCode
	}
	if body.TenantCode != nil {
		in.TenantCode = *body.TenantCode
	}
	for _, id := range body.Identifiers {
		primary := id.Primary != nil && *id.Primary
		in.Identifiers = append(in.Identifiers, domain.Identifier{Type: domain.IdentifierType(id.Type), Value: id.Value, Primary: primary})
	}

	org, err := h.svc.Create(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(org.RowVersion))
	w.Header().Set("Location", "/api/v1/organizations/"+org.ID.String())
	writeJSON(w, http.StatusCreated, toContract(org))
}

// Get implements getOrganization.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionRead)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionRead)
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	org, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(org.RowVersion))
	writeJSON(w, http.StatusOK, toContract(org))
}

// List implements listOrganizations.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionRead)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionRead)
		return
	}
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeValidation(w, r, []domain.FieldError{{Field: "limit", Code: "FORMAT", Message: "1-200 arası tam sayı olmalı"}})
			return
		}
		limit = n
	}
	page, err := h.svc.List(r.Context(), rc, application.ListFilter{
		Role: q.Get("role"), Query: strings.TrimSpace(q.Get("q")), Cursor: q.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.OrganizationPage{Items: make([]kapsorav1.OrganizationSummary, 0, len(page.Items))}
	for _, s := range page.Items {
		out.Items = append(out.Items, kapsorav1.OrganizationSummary{
			Id: s.ID, OrganizationId: s.OrganizationID, DisplayName: s.DisplayName,
			OrganizationKind:   kapsorav1.OrganizationSummaryOrganizationKind(s.OrganizationKind),
			RelationshipRole:   kapsorav1.OrganizationSummaryRelationshipRole(s.RelationshipRole),
			RelationshipStatus: kapsorav1.OrganizationSummaryRelationshipStatus(s.RelationshipStatus),
		})
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

// Update implements updateOrganization (application/merge-patch+json with If-Match).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionManage)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionManage)
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/merge-patch+json") {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type", "UNSUPPORTED_MEDIA_TYPE",
			"Content-Type application/merge-patch+json olmalı", "")
		return
	}
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED",
			"If-Match başlığı gerekli", `GET yanıtındaki ETag değerini If-Match olarak gönderin.`)
		return
	}

	var patch map[string]json.RawMessage
	if !decodeJSON(w, r, &patch) {
		return
	}
	in := application.UpdateInput{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, raw := range patch {
		switch key {
		case "displayName":
			in.DisplayName = decodeString(raw, key, &fields)
		case "relationshipStatus":
			in.RelationshipStatus = decodeString(raw, key, &fields)
		case "tenantCode":
			if isJSONNull(raw) {
				in.ClearTenantCode = true
			} else {
				in.TenantCode = decodeString(raw, key, &fields)
			}
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	org, err := h.svc.Update(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(org.RowVersion))
	writeJSON(w, http.StatusOK, toContract(org))
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrNotFound):
		problem(w, r, http.StatusNotFound, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, application.ErrRelationshipExists):
		problem(w, r, http.StatusConflict, "organization/relationship-exists", "ORGANIZATION_RELATIONSHIP_EXISTS",
			"Bu kurumla bu rolde ilişki zaten var", "")
	case errors.Is(err, application.ErrIdentifierTaken):
		problem(w, r, http.StatusConflict, "organization/identifier-taken", "ORGANIZATION_IDENTIFIER_TAKEN",
			"Tanımlayıcı başka bir kuruma kayıtlı", "")
	case errors.Is(err, application.ErrTenantCodeTaken):
		problem(w, r, http.StatusConflict, "organization/tenant-code-taken", "TENANT_CODE_TAKEN",
			"Bu kurum kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrSharedReadOnly):
		problem(w, r, http.StatusConflict, "organization/shared-readonly", "ORGANIZATION_SHARED_READONLY",
			"Paylaşılan kurumun adı değiştirilemez", "Kurum başka kurumlarla da ilişkili; ad küresel dizinde salt okunur.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		h.logger.Error("organization request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}

func toContract(o application.Organization) kapsorav1.Organization {
	out := kapsorav1.Organization{
		Id: o.ID, OrganizationId: o.OrganizationID,
		LegalName: o.LegalName, DisplayName: o.DisplayName, CountryCode: o.CountryCode,
		OrganizationKind:   kapsorav1.OrganizationOrganizationKind(o.OrganizationKind),
		OrganizationStatus: kapsorav1.OrganizationOrganizationStatus(o.OrganizationStatus),
		RelationshipRole:   kapsorav1.OrganizationRelationshipRole(o.RelationshipRole),
		RelationshipStatus: kapsorav1.OrganizationRelationshipStatus(o.RelationshipStatus),
		TenantCode:         o.TenantCode,
		ValidFrom:          openapi_types.Date{Time: o.ValidFrom},
		RowVersion:         int(o.RowVersion),
		Identifiers:        make([]kapsorav1.OrganizationIdentifier, 0, len(o.Identifiers)),
	}
	if o.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *o.ValidTo}
	}
	for _, id := range o.Identifiers {
		out.Identifiers = append(out.Identifiers, kapsorav1.OrganizationIdentifier{
			Type: kapsorav1.OrganizationIdentifierType(id.Type), MaskedValue: id.MaskedValue, Primary: id.Primary,
		})
	}
	return out
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "organizationId"))
	if err != nil {
		// A malformed id is indistinguishable from an unknown one.
		problem(w, r, http.StatusNotFound, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
		return uuid.Nil, false
	}
	return id, true
}

func etag(version int64) string { return fmt.Sprintf(`"%d"`, version) }

// parseIfMatch accepts "3", W/"3" and 3.
func parseIfMatch(raw string) (int64, bool) {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// isJSONNull recognises an explicit null (merge-patch: "remove this field").
func isJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

func decodeString(raw json.RawMessage, field string, fields *[]domain.FieldError) *string {
	var s string
	if isJSONNull(raw) {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "boş bırakılamaz"})
		return nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	return &s
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
	httpx.WriteProblem(w, r, httpx.Problem{Type: httpx.ProblemTypeBase + typ, Title: title, Status: status, Code: code, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
