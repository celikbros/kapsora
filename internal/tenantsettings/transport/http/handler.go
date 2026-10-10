package tenantsettingshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/tenantsettings/application"
	"github.com/go-chi/chi/v5"
)

const path = "/tenant-settings/accommodation"

var strongETag = regexp.MustCompile(`^"[0-9a-f]{64}"$`)

type Handler struct{ Service *application.Service }

func NewHandler(service *application.Service) *Handler { return &Handler{Service: service} }
func (h *Handler) Routes(r chi.Router, putMiddleware func(http.Handler) http.Handler) {
	r.Get(path, h.Get)
	r.With(h.preflight, putMiddleware).Put(path, h.Put)
}

type commandKey struct{}
type commandContext struct {
	rc  identity.RequestContext
	cmd application.Command
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), "platform.tenant_settings.read")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out, err := h.Service.Read(r.Context(), rc)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeResponse(w, out)
}
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(commandKey{}).(commandContext)
	if !ok {
		h.writeError(w, r, application.ErrInvalid)
		return
	}
	out, err := h.Service.Save(r.Context(), c.rc, c.cmd)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeResponse(w, out)
}
func writeResponse(w http.ResponseWriter, out application.Response) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", out.ETag)
	if out.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}
func strictBody(raw []byte) (application.Command, error) {
	cmd := application.Command{RawBody: raw}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return cmd, application.ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return cmd, application.ErrInvalid
		}
		name, ok := t.(string)
		if !ok || seen[name] {
			return cmd, application.ErrInvalid
		}
		seen[name] = true
		var v json.RawMessage
		if err = d.Decode(&v); err != nil {
			return cmd, application.ErrInvalid
		}
		switch name {
		case "holdMinutes", "quoteTtlMinutes":
			s := string(v)
			if len(s) == 0 || s[0] < '0' || s[0] > '9' {
				return cmd, application.ErrInvalid
			}
			for _, c := range s {
				if c < '0' || c > '9' {
					return cmd, application.ErrInvalid
				}
			}
			n, e := strconv.Atoi(s)
			if e != nil || n < 1 || n > 1440 {
				return cmd, application.ErrInvalid
			}
			if name == "holdMinutes" {
				cmd.HoldMinutes = n
			} else {
				cmd.QuoteTTLMinutes = n
			}
		case "reasonCode":
			if err = json.Unmarshal(v, &cmd.ReasonCode); err != nil {
				return cmd, application.ErrInvalid
			}
			if cmd.ReasonCode != "OPERATING_REQUIREMENT" && cmd.ReasonCode != "CORRECT_CONFIGURATION" {
				return cmd, application.ErrInvalid
			}
		default:
			return cmd, application.ErrInvalid
		}
	}
	t, err = d.Token()
	if err != nil || t != json.Delim('}') || !seen["holdMinutes"] || !seen["quoteTtlMinutes"] || !seen["reasonCode"] {
		return cmd, application.ErrInvalid
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cmd, application.ErrInvalid
	}
	return cmd, nil
}
func (h *Handler) preflight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := identity.RequireStepUp(r.Context(), "platform.tenant_settings.manage")
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		if !rc.Has("platform.tenant_settings.read") {
			h.writeError(w, r, identity.ErrPermissionDenied)
			return
		}
		match := r.Header.Get("If-Match")
		if !strongETag.MatchString(match) {
			problem(w, r, 428, "IF_MATCH_REQUIRED", "If-Match başlığı gerekli")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if len(key) < 16 || len(key) > 128 {
			problem(w, r, 400, "IDEMPOTENCY_KEY_INVALID", "Idempotency-Key geçersiz")
			return
		}
		if strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])) != "application/json" {
			problem(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "İstek biçimi desteklenmiyor")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(raw) > 4096 {
			h.writeError(w, r, application.ErrInvalid)
			return
		}
		cmd, err := strictBody(raw)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		cmd.IfMatch = match
		cmd.Key = key
		r.Body = io.NopCloser(bytes.NewReader(raw))
		prior, err := h.Service.Preflight(r.Context(), rc, cmd)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		if prior != nil {
			writeResponse(w, *prior)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), commandKey{}, commandContext{rc, cmd})))
	})
}

// StoredResultGate is passed to idempotency.Options.BeforeStoredResult. It runs after
// the generic receipt claim may have waited on a concurrent command.
func (h *Handler) StoredResultGate(w http.ResponseWriter, r *http.Request) bool {
	c, ok := r.Context().Value(commandKey{}).(commandContext)
	if !ok {
		h.writeError(w, r, application.ErrInvalid)
		return true
	}
	prior, err := h.Service.Preflight(r.Context(), c.rc, c.cmd)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}
	if prior != nil {
		writeResponse(w, *prior)
		return true
	}
	return false
}
func problem(w http.ResponseWriter, r *http.Request, status int, code, title string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteProblem(w, r, httpx.Problem{Status: status, Code: code, Title: title, Type: httpx.ProblemTypeBase + "tenant-settings/" + strings.ToLower(strings.ReplaceAll(code, "_", "-"))})
}
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		problem(w, r, 401, "UNAUTHENTICATED", "Kimlik doğrulaması gerekli")
	case errors.Is(err, identity.ErrStepUpRequired):
		problem(w, r, 403, "STEP_UP_REQUIRED", "Parola doğrulaması gerekli")
	case errors.Is(err, identity.ErrPermissionDenied):
		problem(w, r, 403, "PERMISSION_DENIED", "Yetki yok")
	case errors.Is(err, application.ErrSensitive):
		problem(w, r, 409, "TENANT_SETTING_CONFIGURATION_CONFLICT", "Ayar yapılandırması çakışıyor")
	case errors.Is(err, application.ErrStale):
		problem(w, r, 412, "ETAG_MISMATCH", "Ayarlar değişti")
	case errors.Is(err, application.ErrKeyReused):
		problem(w, r, 409, "IDEMPOTENCY_KEY_REUSED", "Anahtar farklı istekte kullanıldı")
	case errors.Is(err, application.ErrInvalid):
		problem(w, r, 422, "TENANT_SETTINGS_INVALID", "Geçersiz ayar değeri")
	default:
		problem(w, r, 500, "INTERNAL_ERROR", "Beklenmeyen hata")
	}
}
