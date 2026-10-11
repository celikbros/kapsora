package pricinghttp

import (
	"net/http"
	"strconv"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/pricing/application"
)

func optionFilter(w http.ResponseWriter, r *http.Request) (application.OptionFilter, bool) {
	query := r.URL.Query()
	f := application.OptionFilter{Query: query.Get("q"), Cursor: query.Get("cursor")}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeValidation(w, r, []benefitdomain.FieldError{{Field: "limit", Code: "RANGE", Message: "1-100 olmalı"}})
			return f, false
		}
		f.Limit = limit
	}
	return f, true
}

func (h *Handler) ListProviderOptions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	f, ok := optionFilter(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListProviderOptions(r.Context(), rc, f)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, page)
}

func (h *Handler) ListServiceOptions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	f, ok := optionFilter(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListServiceOptions(r.Context(), rc, f)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, page)
}
