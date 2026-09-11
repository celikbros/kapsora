package benefithttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the entitlement routes (migration 000008).
const (
	PermissionEntitlementRead   = "entitlement.read"
	PermissionEntitlementAdjust = "entitlement.adjust"
)

// EntitlementMiddlewares are the wrappers cmd/api applies; a nil field means "no wrapper".
type EntitlementMiddlewares struct {
	CreateAdjustment func(http.Handler) http.Handler
}

// EntitlementHandler serves the balance, ledger and adjustment operations. It sits beside
// Handler rather than inside it because it drives a different application service; the
// error mapping and the small request helpers are shared through the package.
type EntitlementHandler struct {
	svc    *ledger.Service
	deny   Denier
	logger *slog.Logger
}

// NewEntitlementHandler wires the handler.
func NewEntitlementHandler(svc *ledger.Service, deny Denier, logger *slog.Logger) *EntitlementHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EntitlementHandler{svc: svc, deny: deny, logger: logger}
}

// AccountRoutes mounts everything below /entitlement-accounts.
func (h *EntitlementHandler) AccountRoutes(r chi.Router, mw EntitlementMiddlewares) {
	r.Get("/{accountId}", h.GetAccount)
	r.Get("/{accountId}/ledger", h.ListLedger)
	r.With(wrap(mw.CreateAdjustment)).Post("/{accountId}/adjustments", h.CreateAdjustment)
}

// AdjustmentRoutes mounts everything below /entitlement-adjustments.
func (h *EntitlementHandler) AdjustmentRoutes(r chi.Router) {
	r.Get("/", h.ListAdjustments)
	r.Post("/{adjustmentId}/approve", h.ApproveAdjustment)
	r.Post("/{adjustmentId}/reject", h.RejectAdjustment)
}

// PersonRoutes mounts the person-scoped balance route under /people.
func (h *EntitlementHandler) PersonRoutes(r chi.Router) {
	r.Get("/{personId}/entitlements", h.ListPersonEntitlements)
}

// The entitlement bodies are hand-written rather than taken from the generated contract
// types: a quantity is a numeric(20,6) that must never pass through a float, and
// json.Number keeps the exact decimal text on the wire in both directions. The JSON
// shape is exactly the contract's.

type definitionSummaryView struct {
	Id             uuid.UUID `json:"id"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	UnitType       string    `json:"unitType"`
	CurrencyCode   *string   `json:"currencyCode,omitempty"`
	FamilyShared   bool      `json:"familyShared"`
	AllowOverdraft bool      `json:"allowOverdraft"`
}

type reservationView struct {
	Id               uuid.UUID   `json:"id"`
	ReferenceType    string      `json:"referenceType"`
	ReferenceId      uuid.UUID   `json:"referenceId"`
	Quantity         json.Number `json:"quantity"`
	ConsumedQuantity json.Number `json:"consumedQuantity"`
	ReleasedQuantity json.Number `json:"releasedQuantity"`
	Status           string      `json:"status"`
	ExpiresAt        *time.Time  `json:"expiresAt,omitempty"`
	CreatedAt        time.Time   `json:"createdAt"`
}

type accountView struct {
	Id                uuid.UUID             `json:"id"`
	EnrollmentId      uuid.UUID             `json:"enrollmentId"`
	PersonId          uuid.UUID             `json:"personId"`
	Definition        definitionSummaryView `json:"definition"`
	BenefitPeriodFrom string                `json:"benefitPeriodFrom"`
	BenefitPeriodTo   *string               `json:"benefitPeriodTo"`
	TotalGranted      json.Number           `json:"totalGranted"`
	Available         json.Number           `json:"available"`
	Reserved          json.Number           `json:"reserved"`
	Consumed          json.Number           `json:"consumed"`
	Expired           json.Number           `json:"expired"`
	Status            string                `json:"status"`
	Shared            bool                  `json:"shared"`
	OpenReservations  []reservationView     `json:"openReservations,omitempty"`
	RowVersion        int64                 `json:"rowVersion"`
}

type accountListView struct {
	Items []accountView `json:"items"`
}

type ledgerEntryView struct {
	Id             uuid.UUID   `json:"id"`
	MovementType   string      `json:"movementType"`
	EffectiveAt    time.Time   `json:"effectiveAt"`
	DeltaTotal     json.Number `json:"deltaTotal"`
	DeltaAvailable json.Number `json:"deltaAvailable"`
	DeltaReserved  json.Number `json:"deltaReserved"`
	DeltaConsumed  json.Number `json:"deltaConsumed"`
	DeltaExpired   json.Number `json:"deltaExpired"`
	ReferenceType  string      `json:"referenceType"`
	ReferenceId    uuid.UUID   `json:"referenceId"`
	ReservationId  *uuid.UUID  `json:"reservationId"`
	ReasonCode     *string     `json:"reasonCode"`
	ReasonText     *string     `json:"reasonText"`
	CreatedBy      *uuid.UUID  `json:"createdBy"`
}

type ledgerPageView struct {
	Items      []ledgerEntryView `json:"items"`
	NextCursor *string           `json:"nextCursor"`
}

type adjustmentView struct {
	Id              uuid.UUID   `json:"id"`
	AccountId       uuid.UUID   `json:"accountId"`
	DeltaQuantity   json.Number `json:"deltaQuantity"`
	ReasonCode      string      `json:"reasonCode"`
	ReasonText      *string     `json:"reasonText"`
	Status          string      `json:"status"`
	RequestedBy     uuid.UUID   `json:"requestedBy"`
	RequestedAt     time.Time   `json:"requestedAt"`
	DecidedBy       *uuid.UUID  `json:"decidedBy"`
	DecidedAt       *time.Time  `json:"decidedAt"`
	DecisionComment *string     `json:"decisionComment"`
	LedgerEntryId   *uuid.UUID  `json:"ledgerEntryId"`
	RowVersion      int64       `json:"rowVersion"`
}

type adjustmentPageView struct {
	Items      []adjustmentView `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

type createAdjustmentRequest struct {
	DeltaQuantity json.Number `json:"deltaQuantity"`
	ReasonCode    string      `json:"reasonCode"`
	ReasonText    *string     `json:"reasonText,omitempty"`
}

// ListPersonEntitlements implements listPersonEntitlements.
func (h *EntitlementHandler) ListPersonEntitlements(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEntitlementRead)
	if !ok {
		return
	}
	personID, ok := h.pathUUID(w, r, "personId", ledger.ErrNotFound)
	if !ok {
		return
	}
	asOf, ok := h.queryDate(w, r, "asOf")
	if !ok {
		return
	}
	accounts, err := h.svc.ListPersonEntitlements(r.Context(), rc, personID, asOf)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := accountListView{Items: make([]accountView, 0, len(accounts))}
	for _, a := range accounts {
		out.Items = append(out.Items, newAccountView(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetAccount implements getEntitlementAccount.
func (h *EntitlementHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEntitlementRead)
	if !ok {
		return
	}
	accountID, ok := h.pathUUID(w, r, "accountId", ledger.ErrAccountNotFound)
	if !ok {
		return
	}
	account, err := h.svc.GetAccount(r.Context(), rc, accountID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(account.RowVersion))
	writeJSON(w, http.StatusOK, newAccountView(account))
}

// ListLedger implements listEntitlementLedger.
func (h *EntitlementHandler) ListLedger(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEntitlementRead)
	if !ok {
		return
	}
	accountID, ok := h.pathUUID(w, r, "accountId", ledger.ErrAccountNotFound)
	if !ok {
		return
	}
	page, err := h.svc.ListLedger(r.Context(), rc, accountID, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := ledgerPageView{Items: make([]ledgerEntryView, 0, len(page.Items))}
	for _, e := range page.Items {
		out.Items = append(out.Items, newLedgerEntryView(e))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateAdjustment implements createEntitlementAdjustment.
func (h *EntitlementHandler) CreateAdjustment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEntitlementAdjust)
	if !ok {
		return
	}
	accountID, ok := h.pathUUID(w, r, "accountId", ledger.ErrAccountNotFound)
	if !ok {
		return
	}
	var body createAdjustmentRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	delta, err := domain.ParseQuantity(body.DeltaQuantity.String())
	if err != nil {
		writeValidation(w, r, []domain.FieldError{{
			Field: "deltaQuantity", Code: "FORMAT", Message: "en fazla 6 ondalık basamaklı sayı olmalı",
		}})
		return
	}

	adjustment, err := h.svc.CreateAdjustment(r.Context(), rc, accountID, ledger.NewAdjustmentInput{
		Delta: delta, ReasonCode: body.ReasonCode, ReasonText: body.ReasonText,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(adjustment.RowVersion))
	writeJSON(w, http.StatusCreated, newAdjustmentView(adjustment))
}

// ListAdjustments implements listEntitlementAdjustments.
func (h *EntitlementHandler) ListAdjustments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEntitlementRead)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = ledger.AdjustmentPending
	}
	page, err := h.svc.ListAdjustments(r.Context(), rc, ledger.AdjustmentFilter{
		Status: status, Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := adjustmentPageView{Items: make([]adjustmentView, 0, len(page.Items))}
	for _, a := range page.Items {
		out.Items = append(out.Items, newAdjustmentView(a))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// ApproveAdjustment implements approveEntitlementAdjustment; it needs entitlement.adjust
// and a valid step-up window, and the approver must differ from the requester.
func (h *EntitlementHandler) ApproveAdjustment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionEntitlementAdjust)
	if !ok {
		return
	}
	adjustmentID, ok := h.pathUUID(w, r, "adjustmentId", ledger.ErrAdjustmentNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewComment
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	adjustment, err := h.svc.ApproveAdjustment(r.Context(), rc, adjustmentID, body.Comment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(adjustment.RowVersion))
	writeJSON(w, http.StatusOK, newAdjustmentView(adjustment))
}

// RejectAdjustment implements rejectEntitlementAdjustment.
func (h *EntitlementHandler) RejectAdjustment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireStepUp(w, r, PermissionEntitlementAdjust)
	if !ok {
		return
	}
	adjustmentID, ok := h.pathUUID(w, r, "adjustmentId", ledger.ErrAdjustmentNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReasonCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	adjustment, err := h.svc.RejectAdjustment(r.Context(), rc, adjustmentID, body.ReasonCode, body.ReasonText, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(adjustment.RowVersion))
	writeJSON(w, http.StatusOK, newAdjustmentView(adjustment))
}

// require resolves the request context or writes the denial through the auditing denier.
func (h *EntitlementHandler) require(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// requireStepUp is require plus a valid step-up window (approve and reject).
func (h *EntitlementHandler) requireStepUp(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.RequireStepUp(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// pathUUID reads a path parameter; a malformed id is indistinguishable from an unknown one.
func (h *EntitlementHandler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
}

// queryDate reads an optional YYYY-MM-DD query parameter; a zero time means "today".
func (h *EntitlementHandler) queryDate(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, true
	}
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		writeValidation(w, r, []domain.FieldError{{
			Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı",
		}})
		return time.Time{}, false
	}
	return d, true
}

// writeError maps the ledger errors to problem codes and falls back to the module's
// shared mapping for everything else.
func (h *EntitlementHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrOwnFile):
		h.deny.Deny(w, r, err, "entitlement.adjust")
	case errors.Is(err, ledger.ErrAccountNotFound):
		problem(w, r, http.StatusNotFound, "benefit/entitlement-account-not-found",
			"ENTITLEMENT_ACCOUNT_NOT_FOUND", "Hak hesabı bulunamadı", "")
	case errors.Is(err, ledger.ErrAdjustmentNotFound):
		problem(w, r, http.StatusNotFound, "benefit/entitlement-adjustment-not-found",
			"ENTITLEMENT_ADJUSTMENT_NOT_FOUND", "Hak düzeltmesi bulunamadı", "")
	case errors.Is(err, ledger.ErrReservationNotFound):
		problem(w, r, http.StatusNotFound, "benefit/entitlement-reservation-not-found",
			"ENTITLEMENT_RESERVATION_NOT_FOUND", "Hak rezervasyonu bulunamadı", "")
	case errors.Is(err, ledger.ErrLedgerEntryNotFound), errors.Is(err, ledger.ErrNotFound):
		problem(w, r, http.StatusNotFound, defaultProblemNotFoundID,
			"RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, ledger.ErrAccountFrozen):
		problem(w, r, http.StatusConflict, "benefit/entitlement-account-frozen",
			"ENTITLEMENT_ACCOUNT_FROZEN", "Hak hesabı donduruldu",
			"Mutabakat işi bu hesapta fark buldu; düzeltme onaylanana kadar harcama yapılamaz.")
	case errors.Is(err, ledger.ErrAccountClosed):
		problem(w, r, http.StatusConflict, "benefit/entitlement-account-closed",
			"ENTITLEMENT_ACCOUNT_CLOSED", "Hak hesabı kapalı", "")
	case errors.Is(err, ledger.ErrInsufficient):
		problem(w, r, http.StatusConflict, "benefit/entitlement-insufficient",
			"ENTITLEMENT_INSUFFICIENT", "Yeterli hak bakiyesi yok", "")
	case errors.Is(err, ledger.ErrIdempotencyKeyReuse):
		problem(w, r, http.StatusConflict, "generic/idempotency-key-reused",
			"IDEMPOTENCY_KEY_REUSED", "Bu anahtar farklı bir istekle kullanılmış", "")
	case errors.Is(err, ledger.ErrReservationClosed), errors.Is(err, ledger.ErrQuantityRemainder):
		problem(w, r, http.StatusConflict, "benefit/entitlement-reservation-closed",
			"ENTITLEMENT_RESERVATION_CLOSED", "Rezervasyon bu işlem için uygun değil", "")
	case errors.Is(err, ledger.ErrAdjustmentNotPending):
		problem(w, r, http.StatusConflict, "benefit/entitlement-adjustment-decided",
			"ENTITLEMENT_ADJUSTMENT_DECIDED", "Bu düzeltme zaten karara bağlandı", "")
	case errors.Is(err, ledger.ErrMakerCheckerSame):
		problem(w, r, http.StatusForbidden, "benefit/maker-checker-same-actor",
			"MAKER_CHECKER_SAME_ACTOR", "Onaylayan, talep edenden farklı olmalı",
			"Düzeltmeyi talep eden kullanıcı onu onaylayamaz.")
	case errors.Is(err, ledger.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		writeBenefitError(h.logger, w, r, err)
	}
}

func newAccountView(a ledger.Account) accountView {
	out := accountView{
		Id: a.ID, EnrollmentId: a.EnrollmentID, PersonId: a.PersonID,
		Definition: definitionSummaryView{
			Id: a.Definition.ID, Code: a.Definition.Code, Name: a.Definition.Name,
			UnitType: a.Definition.UnitType, FamilyShared: a.Definition.FamilyShared,
			AllowOverdraft: a.Definition.AllowOverdraft,
		},
		BenefitPeriodFrom: a.PeriodFrom.Format(time.DateOnly),
		TotalGranted:      json.Number(a.Balances.Total.String()),
		Available:         json.Number(a.Balances.Available.String()),
		Reserved:          json.Number(a.Balances.Reserved.String()),
		Consumed:          json.Number(a.Balances.Consumed.String()),
		Expired:           json.Number(a.Balances.Expired.String()),
		Status:            a.Status, Shared: a.Shared, RowVersion: a.RowVersion,
	}
	if a.Definition.CurrencyCode != "" {
		code := a.Definition.CurrencyCode
		out.Definition.CurrencyCode = &code
	}
	if a.PeriodTo != nil {
		to := a.PeriodTo.Format(time.DateOnly)
		out.BenefitPeriodTo = &to
	}
	for _, res := range a.OpenReservations {
		out.OpenReservations = append(out.OpenReservations, reservationView{
			Id: res.ID, ReferenceType: res.ReferenceType, ReferenceId: res.ReferenceID,
			Quantity:         json.Number(res.Quantity.String()),
			ConsumedQuantity: json.Number(res.Consumed.String()),
			ReleasedQuantity: json.Number(res.Released.String()),
			Status:           res.Status, ExpiresAt: res.ExpiresAt, CreatedAt: res.CreatedAt,
		})
	}
	return out
}

func newLedgerEntryView(e ledger.Entry) ledgerEntryView {
	return ledgerEntryView{
		Id: e.ID, MovementType: e.MovementType, EffectiveAt: e.EffectiveAt,
		DeltaTotal:     json.Number(e.Deltas.Total.String()),
		DeltaAvailable: json.Number(e.Deltas.Available.String()),
		DeltaReserved:  json.Number(e.Deltas.Reserved.String()),
		DeltaConsumed:  json.Number(e.Deltas.Consumed.String()),
		DeltaExpired:   json.Number(e.Deltas.Expired.String()),
		ReferenceType:  e.ReferenceType, ReferenceId: e.ReferenceID,
		ReservationId: e.ReservationID, ReasonCode: e.ReasonCode, ReasonText: e.ReasonText,
		CreatedBy: e.CreatedBy,
	}
}

func newAdjustmentView(a ledger.Adjustment) adjustmentView {
	return adjustmentView{
		Id: a.ID, AccountId: a.AccountID, DeltaQuantity: json.Number(a.Delta.String()),
		ReasonCode: a.ReasonCode, ReasonText: a.ReasonText, Status: a.Status,
		RequestedBy: a.RequestedBy, RequestedAt: a.RequestedAt, DecidedBy: a.DecidedBy,
		DecidedAt: a.DecidedAt, DecisionComment: a.DecisionComment,
		LedgerEntryId: a.LedgerEntryID, RowVersion: a.RowVersion,
	}
}
