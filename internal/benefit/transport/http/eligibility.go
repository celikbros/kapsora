package benefithttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
)

// PermissionEligibilityCheck guards both eligibility routes (migration 000008).
const PermissionEligibilityCheck = "eligibility.check"

// EligibilityHandler serves POST /eligibility/checks and
// GET /eligibility/evaluations/{id}. It sits beside Handler for the same reason the
// entitlement handler does: a different application service, the same error mapping and
// request helpers.
type EligibilityHandler struct {
	svc    *eligibility.Service
	deny   Denier
	logger *slog.Logger
}

// NewEligibilityHandler wires the handler.
func NewEligibilityHandler(svc *eligibility.Service, deny Denier, logger *slog.Logger) *EligibilityHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EligibilityHandler{svc: svc, deny: deny, logger: logger}
}

// Routes mounts everything below /eligibility.
//
// The platform Idempotency-Key middleware is deliberately not applied here. A check
// changes no business state, and its replay contract belongs to the evaluation table
// itself: benefit.eligibility_evaluation carries the key with a partial unique index, so
// the stored snapshot is the replayed answer and its request hash is what tells a repeat
// of the same question from a different question under a reused key. Wrapping the route
// in the middleware would store a second copy of the response in
// system.idempotency_record and answer from there instead.
func (h *EligibilityHandler) Routes(r chi.Router) {
	r.Post("/checks", h.Check)
	r.Get("/evaluations/{evaluationId}", h.GetEvaluation)
}

// The request body is hand-written rather than taken from the generated contract types
// because its quantities are numeric(20,6): json.Number keeps the exact decimal text, so
// no requested quantity ever passes through a float. The JSON shape is the contract's.
type eligibilityCheckItem struct {
	ServiceDefinitionId uuid.UUID    `json:"serviceDefinitionId"`
	Quantity            json.Number  `json:"quantity"`
	RequestedAmount     *json.Number `json:"requestedAmount,omitempty"`
	CurrencyCode        *string      `json:"currencyCode,omitempty"`
}

type eligibilityCheckRequest struct {
	PersonId               uuid.UUID              `json:"personId"`
	ProgramId              *uuid.UUID             `json:"programId,omitempty"`
	EnrollmentId           *uuid.UUID             `json:"enrollmentId,omitempty"`
	ProviderOrganizationId *uuid.UUID             `json:"providerOrganizationId,omitempty"`
	ServiceDate            string                 `json:"serviceDate"`
	ServiceItems           []eligibilityCheckItem `json:"serviceItems"`
	Context                map[string]any         `json:"context,omitempty"`
}

// Check implements checkEligibility.
func (h *EligibilityHandler) Check(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEligibilityCheck)
	if !ok {
		return
	}
	var body eligibilityCheckRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in, ok := checkInput(w, r, body)
	if !ok {
		return
	}
	in.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	result, err := h.svc.Check(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetEvaluation implements getEligibilityEvaluation.
func (h *EligibilityHandler) GetEvaluation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEligibilityCheck)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "evaluationId", eligibility.ErrEvaluationNotFound)
	if !ok {
		return
	}
	evaluation, err := h.svc.GetEvaluation(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, evaluation)
}

// checkInput turns the decoded body into the application input, answering 422 for a bad
// date or a quantity that is not an exact numeric(20,6).
func checkInput(w http.ResponseWriter, r *http.Request, body eligibilityCheckRequest) (eligibility.CheckInput, bool) {
	ve := &domain.ValidationError{}
	in := eligibility.CheckInput{
		PersonID: body.PersonId, ProgramID: body.ProgramId, EnrollmentID: body.EnrollmentId,
		ProviderOrganizationID: body.ProviderOrganizationId, Context: body.Context,
		Items: make([]eligibility.RequestItem, 0, len(body.ServiceItems)),
	}
	if day, err := time.Parse(time.DateOnly, body.ServiceDate); err != nil {
		ve.Add("serviceDate", "FORMAT", "YYYY-MM-DD biçiminde olmalı")
	} else {
		in.ServiceDate = day
	}
	for i, item := range body.ServiceItems {
		field := "serviceItems[" + strconv.Itoa(i) + "]"
		out := eligibility.RequestItem{ServiceDefinitionID: item.ServiceDefinitionId}
		quantity, err := domain.ParseQuantity(item.Quantity.String())
		if err != nil {
			ve.Add(field+".quantity", "FORMAT", "en fazla 6 ondalık basamaklı sayı olmalı")
		} else {
			out.Quantity = quantity
		}
		if item.RequestedAmount != nil {
			amount, err := domain.ParseQuantity(item.RequestedAmount.String())
			if err != nil {
				ve.Add(field+".requestedAmount", "FORMAT", "en fazla 6 ondalık basamaklı sayı olmalı")
			} else {
				out.RequestedAmount = &amount
			}
		}
		if item.CurrencyCode != nil {
			out.CurrencyCode = *item.CurrencyCode
		}
		in.Items = append(in.Items, out)
	}
	if err := ve.OrNil(); err != nil {
		writeValidation(w, r, ve.Fields)
		return eligibility.CheckInput{}, false
	}
	return in, true
}

// require resolves the request context or writes the denial through the auditing denier.
func (h *EligibilityHandler) require(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// pathUUID reads a path parameter; a malformed id is indistinguishable from an unknown one.
func (h *EligibilityHandler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
}

// writeError maps the eligibility errors to problem codes and falls back to the module's
// shared mapping for everything else.
func (h *EligibilityHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, eligibility.ErrEvaluationNotFound):
		problem(w, r, http.StatusNotFound, "benefit/eligibility-evaluation-not-found",
			"ELIGIBILITY_EVALUATION_NOT_FOUND", "Uygunluk değerlendirmesi bulunamadı", "")
	case errors.Is(err, eligibility.ErrIdempotencyKeyReuse):
		problem(w, r, http.StatusConflict, "generic/idempotency-key-reused",
			"IDEMPOTENCY_KEY_REUSED", "Bu anahtar farklı bir istekle kullanılmış", "")
	case errors.Is(err, eligibility.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "identity/permission-denied", "PERMISSION_DENIED",
			"Bu işlem için yetkiniz yok", "Sağlayıcı kapsamınız dışında bir kurum için sorgu yapılamaz.")
	default:
		writeBenefitError(h.logger, w, r, err)
	}
}
