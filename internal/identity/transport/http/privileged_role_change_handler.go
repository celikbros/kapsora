package identityhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/celikbros/kapsora/internal/platform/idempotency"
)

type PrivilegedRoleChangeHandler struct {
	svc        *application.PrivilegedRoleChangeService
	middleware *Middleware
	logger     *slog.Logger
}

func NewPrivilegedRoleChangeHandler(svc *application.PrivilegedRoleChangeService, middleware *Middleware, logger *slog.Logger) *PrivilegedRoleChangeHandler {
	return &PrivilegedRoleChangeHandler{svc, middleware, logger}
}

func (h *PrivilegedRoleChangeHandler) UserRoutes(r chi.Router, create func(http.Handler) http.Handler) {
	r.With(roleAssignmentNoStore).Get("/{membershipId}/role-change-eligibility", h.Eligibility)
	r.With(roleAssignmentNoStore, h.preflight("CREATE"), create).Post("/{membershipId}/role-change-requests", h.Command)
}
func (h *PrivilegedRoleChangeHandler) CatalogRoutes(r chi.Router, approve, reject, cancel func(http.Handler) http.Handler) {
	r.With(roleAssignmentNoStore).Get("/privileged-role-assignment-options", h.Options)
	r.With(roleAssignmentNoStore).Get("/role-change-requests", h.List)
	r.With(roleAssignmentNoStore).Get("/role-change-requests/{requestId}", h.Detail)
	r.With(roleAssignmentNoStore, h.preflight("APPROVE"), approve).Post("/role-change-requests/{requestId}/approve", h.Command)
	r.With(roleAssignmentNoStore, h.preflight("REJECT"), reject).Post("/role-change-requests/{requestId}/reject", h.Command)
	r.With(roleAssignmentNoStore, h.preflight("CANCEL"), cancel).Post("/role-change-requests/{requestId}/cancel", h.Command)
}

type roleChangeCommandContextKey struct{}
type roleChangeCommandContext struct {
	rc  identity.RequestContext
	cmd application.RoleChangeCommand
}

// StoredResultGate runs after generic idempotency's claim transaction may have
// waited. It refreshes authorization before either generic replay or IN_PROGRESS,
// and prefers the business-transaction receipt whenever one exists.
func (h *PrivilegedRoleChangeHandler) StoredResultGate(w http.ResponseWriter, r *http.Request) bool {
	validated, ok := r.Context().Value(roleChangeCommandContextKey{}).(roleChangeCommandContext)
	if !ok {
		h.writeError(w, r, application.ErrRoleAssignmentInvalid)
		return true
	}
	replay, err := h.svc.Repo.Preflight(r.Context(), validated.rc, validated.cmd)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}
	if replay != nil {
		h.writeResponse(w, *replay)
		return true
	}
	return false
}

func strictRoleChangeBody(raw []byte, code string, cmd *application.RoleChangeCommand) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return application.ErrRoleAssignmentInvalid
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return application.ErrRoleAssignmentInvalid
		}
		key, ok := keyToken.(string)
		if !ok {
			return application.ErrRoleAssignmentInvalid
		}
		if _, duplicate := fields[key]; duplicate {
			return application.ErrRoleAssignmentInvalid
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return application.ErrRoleAssignmentInvalid
		}
		fields[key] = value
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return application.ErrRoleAssignmentInvalid
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return application.ErrRoleAssignmentInvalid
	}
	allowed := map[string]bool{}
	required := map[string]bool{}
	switch code {
	case "CREATE":
		var operation string
		if err := json.Unmarshal(fields["operation"], &operation); err != nil {
			return application.ErrRoleAssignmentInvalid
		}
		cmd.Operation = operation
		switch operation {
		case "ASSIGN":
			for _, k := range []string{"operation", "roleCode", "configurationHash", "reasonCode"} {
				allowed[k] = true
				required[k] = true
			}
		case "REVOKE":
			for _, k := range []string{"operation", "grantId", "configurationHash", "reasonCode"} {
				allowed[k] = true
				required[k] = true
			}
		default:
			return application.ErrRoleAssignmentInvalid
		}
	case "APPROVE": // The only legal body is an empty object.
	case "REJECT", "CANCEL":
		allowed["reasonCode"] = true
		required["reasonCode"] = true
	default:
		return application.ErrRoleAssignmentInvalid
	}
	for k := range fields {
		if !allowed[k] {
			return application.ErrRoleAssignmentInvalid
		}
	}
	for k := range required {
		if _, ok := fields[k]; !ok {
			return application.ErrRoleAssignmentInvalid
		}
	}
	stringField := func(k string) (string, error) {
		var s string
		if err := json.Unmarshal(fields[k], &s); err != nil || strings.TrimSpace(s) != s || s == "" {
			return "", application.ErrRoleAssignmentInvalid
		}
		return s, nil
	}
	if code == "CREATE" {
		if cmd.Operation == "ASSIGN" {
			cmd.RoleCode, err = stringField("roleCode")
			if err != nil {
				return err
			}
		} else {
			id, err := stringField("grantId")
			if err != nil {
				return err
			}
			cmd.GrantID, err = uuid.Parse(id)
			if err != nil {
				return application.ErrRoleAssignmentInvalid
			}
		}
		cmd.ConfigurationHash, err = stringField("configurationHash")
		if err != nil {
			return err
		}
		if len(cmd.ConfigurationHash) != 64 || strings.ToLower(cmd.ConfigurationHash) != cmd.ConfigurationHash {
			return application.ErrRoleAssignmentInvalid
		}
		if _, err = hex.DecodeString(cmd.ConfigurationHash); err != nil {
			return application.ErrRoleAssignmentInvalid
		}
	}
	if code != "APPROVE" {
		cmd.ReasonCode, err = stringField("reasonCode")
		if err != nil {
			return err
		}
	}
	switch code {
	case "CREATE":
		if cmd.Operation == "ASSIGN" && cmd.ReasonCode != "ONBOARDING" && cmd.ReasonCode != "DUTY_ASSIGNMENT" {
			return application.ErrRoleAssignmentInvalid
		}
		if cmd.Operation == "REVOKE" && cmd.ReasonCode != "ACCESS_REVIEW" && cmd.ReasonCode != "DUTY_ENDED" && cmd.ReasonCode != "SECURITY_CONCERN" {
			return application.ErrRoleAssignmentInvalid
		}
	case "REJECT":
		if cmd.ReasonCode != "NOT_JUSTIFIED" && cmd.ReasonCode != "INCORRECT_ACCESS" && cmd.ReasonCode != "STALE_REQUEST" {
			return application.ErrRoleAssignmentInvalid
		}
	case "CANCEL":
		if cmd.ReasonCode != "WITHDRAWN" {
			return application.ErrRoleAssignmentInvalid
		}
	}
	return nil
}

func (h *PrivilegedRoleChangeHandler) preflight(code string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc, err := identity.RequireStepUp(r.Context(), "identity.role.manage")
			if err != nil {
				h.writeError(w, r, err)
				return
			}
			cmd := application.RoleChangeCommand{Code: code}
			param := "requestId"
			if code == "CREATE" {
				param = "membershipId"
			}
			id, err := uuid.Parse(chi.URLParam(r, param))
			if err != nil {
				h.writeError(w, r, application.ErrRoleAssignmentNotFound)
				return
			}
			if code == "CREATE" {
				cmd.TargetMembershipID = id
			} else {
				cmd.RequestID = id
			}
			match := r.Header.Get("If-Match")
			cmd.Version, err = strconv.ParseInt(strings.Trim(match, `"`), 10, 64)
			if err != nil || cmd.Version < 1 || match != fmt.Sprintf(`"%d"`, cmd.Version) {
				problem(w, r, 428, "generic/precondition-required", "IF_MATCH_REQUIRED", "If-Match başlığı gerekli", "")
				return
			}
			key := strings.TrimSpace(r.Header.Get(idempotency.KeyHeader))
			if key == "" {
				problem(w, r, 400, "generic/idempotency-key-required", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key gerekli", "")
				return
			}
			if len(key) < 16 || len(key) > 128 {
				problem(w, r, 400, "generic/idempotency-key-invalid", "IDEMPOTENCY_KEY_INVALID", "Idempotency-Key geçersiz", "")
				return
			}
			if strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])) != "application/json" {
				problem(w, r, 415, "generic/unsupported-media-type", "UNSUPPORTED_MEDIA_TYPE", "İstek biçimi desteklenmiyor", "")
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
			if err != nil || len(body) > 4096 {
				h.writeError(w, r, application.ErrRoleAssignmentInvalid)
				return
			}
			if err := strictRoleChangeBody(body, code, &cmd); err != nil {
				h.writeError(w, r, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			keySum := sha256.Sum256([]byte(key))
			cmd.KeyHash = keySum[:]
			cmd.RequestHash = idempotency.Fingerprint(r, idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, body, []string{"If-Match"})
			replay, err := h.svc.Repo.Preflight(r.Context(), rc, cmd)
			if err != nil {
				h.writeError(w, r, err)
				return
			}
			if replay != nil {
				h.writeResponse(w, *replay)
				return
			}
			ctx := context.WithValue(r.Context(), roleChangeCommandContextKey{}, roleChangeCommandContext{rc, cmd})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (h *PrivilegedRoleChangeHandler) Command(w http.ResponseWriter, r *http.Request) {
	validated, ok := r.Context().Value(roleChangeCommandContextKey{}).(roleChangeCommandContext)
	if !ok {
		h.writeError(w, r, application.ErrRoleAssignmentInvalid)
		return
	}
	result, err := h.svc.Repo.Execute(r.Context(), validated.rc, validated.cmd)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeResponse(w, result)
}
func (h *PrivilegedRoleChangeHandler) writeResponse(w http.ResponseWriter, out application.RoleChangeResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", out.ETag)
	if out.Replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}

func (h *PrivilegedRoleChangeHandler) require(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), "identity.role.manage")
	if err == nil {
		err = h.svc.Repo.Authorize(r.Context(), rc, false)
	}
	if err != nil {
		h.writeError(w, r, err)
		return identity.RequestContext{}, false
	}
	return rc, true
}
func (h *PrivilegedRoleChangeHandler) Options(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	items, err := h.svc.Repo.Options(r.Context(), rc)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, struct {
		Items []application.PrivilegedRoleOption `json:"items"`
	}{items})
}
func (h *PrivilegedRoleChangeHandler) Eligibility(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "membershipId"))
	if err != nil {
		h.writeError(w, r, application.ErrRoleAssignmentNotFound)
		return
	}
	out, err := h.svc.Repo.Eligibility(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(out.MembershipRowVersion))
	writeJSON(w, out)
}
func (h *PrivilegedRoleChangeHandler) List(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "PENDING"
	}
	if status != "PENDING" && status != "APPROVED" && status != "REJECTED" && status != "CANCELLED" {
		h.badQuery(w, r)
		return
	}
	var member *uuid.UUID
	if raw := r.URL.Query().Get("membershipId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			h.badQuery(w, r)
			return
		}
		member = &id
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
	cur, present, err := h.svc.Cursors.Decode(r.URL.Query().Get("cursor"))
	if err != nil {
		h.badQuery(w, r)
		return
	}
	var after *httpx.Cursor
	if present {
		after = &cur
	}
	pageSize := httpx.ClampLimit(limit)
	items, err := h.svc.Repo.List(r.Context(), rc, status, member, after, pageSize+1)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var next *string
	if len(items) > pageSize {
		items = items[:pageSize]
		last := items[len(items)-1]
		s := h.svc.Cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		next = &s
	}
	writeJSON(w, application.RoleChangeRequestPage{Items: items, NextCursor: next})
}
func (h *PrivilegedRoleChangeHandler) Detail(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "requestId"))
	if err != nil {
		h.writeError(w, r, application.ErrRoleAssignmentNotFound)
		return
	}
	out, err := h.svc.Repo.Detail(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(out.Request.RowVersion))
	writeJSON(w, out)
}
func (h *PrivilegedRoleChangeHandler) badQuery(w http.ResponseWriter, r *http.Request) {
	problem(w, r, 400, "identity/role-change-query-invalid", "ROLE_CHANGE_QUERY_INVALID", "Geçersiz liste isteği", "")
}
func (h *PrivilegedRoleChangeHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated), errors.Is(err, identity.ErrPermissionDenied), errors.Is(err, identity.ErrStepUpRequired):
		h.middleware.Deny(w, r, err, "identity.role.manage")
	case errors.Is(err, application.ErrRoleAssignmentInvalid):
		problem(w, r, 400, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
	case errors.Is(err, application.ErrRoleAssignmentNotFound):
		problem(w, r, 404, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, httpx.ErrInvalidCursor):
		h.badQuery(w, r)
	case errors.Is(err, application.ErrRoleAssignmentVersion):
		problem(w, r, 412, "generic/etag-mismatch", "ETAG_MISMATCH", "Kaynak değişti", "")
	case errors.Is(err, application.ErrRoleChangeKeyReused):
		problem(w, r, 409, "generic/idempotency-key-reused", "IDEMPOTENCY_KEY_REUSED", "Anahtar farklı komutta kullanıldı", "")
	case errors.Is(err, application.ErrRoleChangeNoChecker):
		problem(w, r, 409, "identity/no-eligible-checker", "NO_ELIGIBLE_CHECKER", "Uygun onaylayıcı yok", "")
	case errors.Is(err, application.ErrRoleChangePendingExists):
		problem(w, r, 409, "identity/role-change-pending-exists", "ROLE_CHANGE_PENDING_EXISTS", "Bekleyen talep var", "")
	case errors.Is(err, application.ErrRoleChangeNotPending):
		problem(w, r, 409, "identity/role-change-not-pending", "ROLE_CHANGE_NOT_PENDING", "Talep beklemede değil", "")
	case errors.Is(err, application.ErrRoleChangeTargetChanged):
		problem(w, r, 409, "identity/role-change-target-changed", "ROLE_CHANGE_TARGET_CHANGED", "Hedef değişti", "")
	case errors.Is(err, application.ErrRoleChangeConfigurationChanged):
		problem(w, r, 409, "identity/role-change-configuration-changed", "ROLE_CHANGE_CONFIGURATION_CHANGED", "Rol yapısı değişti", "")
	case errors.Is(err, application.ErrRoleChangeMakerUnauthorized):
		problem(w, r, 409, "identity/role-change-maker-unauthorized", "ROLE_CHANGE_MAKER_UNAUTHORIZED", "Talep sahibi artık yetkili değil", "")
	case errors.Is(err, application.ErrRoleChangeSameActor):
		problem(w, r, 403, "identity/maker-checker-same-actor", "MAKER_CHECKER_SAME_ACTOR", "Talep ve karar farklı kişilerden olmalı", "")
	case errors.Is(err, application.ErrRoleChangeCancelForbidden):
		problem(w, r, 403, "identity/role-change-cancel-forbidden", "ROLE_CHANGE_CANCEL_FORBIDDEN", "Talebi yalnız sahibi iptal edebilir", "")
	case errors.Is(err, application.ErrRoleAssignmentSelf):
		problem(w, r, 409, "identity/self-role-change-forbidden", "SELF_ROLE_CHANGE_FORBIDDEN", "Kendi rolünüz değiştirilemez", "")
	case errors.Is(err, application.ErrExistingAccessConflict):
		problem(w, r, 409, "identity/existing-access-conflict", "EXISTING_ACCESS_CONFLICT", "Mevcut erişim çakışıyor", "")
	case errors.Is(err, application.ErrRoleGrantConflict):
		problem(w, r, 409, "identity/grant-state-conflict", "GRANT_STATE_CONFLICT", "Erişim güncel değil", "")
	case errors.Is(err, application.ErrRoleConfigurationUnsupported):
		problem(w, r, 409, "identity/role-configuration-unsupported", "ROLE_CONFIGURATION_UNSUPPORTED", "Rol desteklenmiyor", "")
	case errors.Is(err, application.ErrDirectoryLastManager):
		problem(w, r, 409, "identity/last-tenant-manager", "LAST_TENANT_MANAGER", "Son yönetici kaldırılamaz", "")
	case errors.Is(err, application.ErrLastTenantRoleManager):
		problem(w, r, 409, "identity/last-tenant-role-manager", "LAST_TENANT_ROLE_MANAGER", "Son rol yöneticisi kaldırılamaz", "")
	default:
		h.logger.Error("privileged role change failed", "error", err)
		problem(w, r, 500, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}
