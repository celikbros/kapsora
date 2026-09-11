// Package accommodationhttp serves /api/v1/accommodation. Bodies use the generated contract
// types; errors are problem+json with stable codes and Turkish titles (ADR-015).
//
// One rule shapes every route here: **no handler in this package decides who a request is
// for**. The provider boundary is computed in the application service from the caller's
// grants, and the person a search is about comes from the caller's PERSON binding through
// identity.RequirePerson. A handler that read either of those out of the body would be a
// handler through which a member could search — and later hold — a room for their neighbour.
package accommodationhttp

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

	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. Reading a hotel and opening its allotment are two grants
// on purpose: `accommodation.property.read` is what a member has, and
// `accommodation.inventory.manage` is what it deliberately does not.
const (
	PermissionRead   = application.PermissionRead
	PermissionManage = application.PermissionManage
)

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateProperty func(http.Handler) http.Handler
	PatchProperty  func(http.Handler) http.Handler
	CreateRoomType func(http.Handler) http.Handler
	PatchRoomType  func(http.Handler) http.Handler
	PutInventory   func(http.Handler) http.Handler
	Search         func(http.Handler) http.Handler
}

// Handler serves the accommodation operations.
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

// PropertyRoutes mounts everything below /accommodation/properties.
func (h *Handler) PropertyRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListProperties)
	r.With(wrap(mw.CreateProperty)).Post("/", h.CreateProperty)
	r.Get("/{propertyId}", h.GetProperty)
	r.With(wrap(mw.PatchProperty)).Patch("/{propertyId}", h.PatchProperty)
	r.Get("/{propertyId}/room-types", h.ListRoomTypes)
	r.With(wrap(mw.CreateRoomType)).Post("/{propertyId}/room-types", h.CreateRoomType)
}

// RoomTypeRoutes mounts everything below /accommodation/room-types.
func (h *Handler) RoomTypeRoutes(r chi.Router, mw Middlewares) {
	r.With(wrap(mw.PatchRoomType)).Patch("/{roomTypeId}", h.PatchRoomType)
	r.Get("/{roomTypeId}/inventory", h.GetRoomTypeInventory)
	r.With(wrap(mw.PutInventory)).Put("/{roomTypeId}/inventory", h.PutRoomTypeInventory)
}

// AvailabilityRoutes mounts everything below /accommodation/availability.
func (h *Handler) AvailabilityRoutes(r chi.Router, mw Middlewares) {
	r.With(wrap(mw.Search)).Post("/search", h.SearchAvailability)
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

// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	var below *application.InventoryBelowCommitment
	var unavailable *application.RoomUnavailable
	var unpriceable *application.QuoteUnavailable
	var checkInWindow *application.CheckInWindowClosed
	switch {
	case errors.Is(err, identity.ErrOwnFile):
		h.deny.Deny(w, r, err, "accommodation.no_show.review")
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.As(err, &below):
		// The refusal this package exists for, with the one fact that makes it actionable:
		// a provider opening ninety nights is told which night to look at, as an RFC 9457
		// extension member rather than as a sentence somebody has to parse.
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "accommodation/inventory-below-commitment",
			Title:  "Kontenjan, verilmiş sözden az olamaz",
			Status: http.StatusConflict, Code: "INVENTORY_BELOW_COMMITMENT",
			Detail: "Bu gecede zaten tutulmuş veya onaylanmış oda sayısı, girilen kontenjandan fazla.",
			Extensions: map[string]any{
				"stayDate":  below.StayDate.Format(time.DateOnly),
				"capacity":  below.Capacity,
				"held":      below.Held,
				"confirmed": below.Confirmed,
			},
		})
	case errors.As(err, &unavailable):
		// The refusal the booking half exists for, with the one fact that makes it
		// actionable: a member looking at a fortnight is told which night to move rather
		// than that something somewhere failed. `allotted` separates a night that is full
		// from one the provider never opened, which are different things to a clerk.
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "accommodation/room-unavailable",
			Title:  "Bu tarihlerde boş oda yok",
			Status: http.StatusConflict, Code: "ROOM_UNAVAILABLE",
			Detail: "Konaklamanın en az bir gecesinde bu oda tipinden boş oda kalmadı.",
			Extensions: map[string]any{
				"stayDate":  unavailable.StayDate.Format(time.DateOnly),
				"allotted":  unavailable.Allotted,
				"capacity":  unavailable.Capacity,
				"held":      unavailable.Held,
				"confirmed": unavailable.Confirmed,
			},
		})
	case errors.As(err, &unpriceable):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "accommodation/quote-unavailable",
			Title:  "Bu tarihler için fiyat bulunamadı",
			Status: http.StatusConflict, Code: "QUOTE_UNAVAILABLE",
			Detail:     "Sözleşmede bu oda tipi için konaklamanın tüm gecelerini kapsayan fiyat yok.",
			Extensions: map[string]any{"reason": unpriceable.Reason},
		})
	case errors.As(err, &checkInWindow):
		// The refusal a desk can act on: when the window opens, when it closes, and the zone
		// both are counted in. A clerk told only "outside the window" has been told nothing,
		// and one who can see the window closed yesterday knows to raise a no-show instead.
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "accommodation/check-in-window",
			Title:  "Giriş saati aralığının dışında",
			Status: http.StatusConflict, Code: "BOOKING_CHECK_IN_WINDOW",
			Detail: "Bu rezervasyon için giriş yalnızca tesisin kendi saatiyle belirlenen " +
				"aralıkta kaydedilebilir.",
			Extensions: map[string]any{
				"opensAt":  checkInWindow.OpensAt.Format(time.RFC3339),
				"closesAt": checkInWindow.ClosesAt.Format(time.RFC3339),
				"timezone": checkInWindow.TimeZone,
			},
		})
	case errors.Is(err, application.ErrBookingNotFound):
		problem(w, r, http.StatusNotFound, "accommodation/booking-not-found",
			"BOOKING_NOT_FOUND", "Rezervasyon bulunamadı", "")
	case errors.Is(err, application.ErrBookingAlreadyLive):
		problem(w, r, http.StatusConflict, "accommodation/booking-already-live",
			"BOOKING_ALREADY_LIVE", "Bu tarihte bu oda tipinde açık bir rezervasyonunuz var",
			"Aynı kişi, aynı oda tipi ve aynı giriş tarihi için tek bir açık rezervasyon olabilir.")
	case errors.Is(err, application.ErrBookingTransitionInvalid):
		problem(w, r, http.StatusConflict, "accommodation/booking-transition-invalid",
			"BOOKING_TRANSITION_INVALID", "Rezervasyon bu işlem için uygun durumda değil",
			"Rezervasyonun güncel durumunu alıp yeniden deneyin.")
	case errors.Is(err, application.ErrQuoteStale):
		problem(w, r, http.StatusConflict, "accommodation/quote-stale", "QUOTE_STALE",
			"Fiyat teklifi güncelliğini yitirdi",
			"Aramayı yenileyip odayı yeniden seçin; onaylanan tutar gördüğünüz tutar olmalı.")
	case errors.Is(err, application.ErrLodgingTermsMissing):
		problem(w, r, http.StatusConflict, "accommodation/lodging-terms-missing",
			"LODGING_TERMS_MISSING", "Sözleşmede konaklama koşulları tanımlı değil",
			"İptal ve iade koşulları tanımlanmadan rezervasyon onaylanamaz.")
	case errors.Is(err, application.ErrEntitlementAccountNotFound):
		problem(w, r, http.StatusConflict, "accommodation/entitlement-account-not-found",
			"ENTITLEMENT_ACCOUNT_NOT_FOUND", "Bu oda tipi için kullanılabilir hak bulunamadı",
			"Planınızda bu hizmete karşılık gelen ve yeterli bakiyesi olan bir hak yok.")
	case errors.Is(err, application.ErrEntitlementInsufficient):
		problem(w, r, http.StatusConflict, "accommodation/entitlement-insufficient",
			"ENTITLEMENT_INSUFFICIENT", "Planınız bu konaklamanın hiçbir gecesini karşılamıyor",
			"Bu hizmet planınızda tanımlı değil ya da konaklama hakkınız tükendi; "+
				"planın karşılamadığı bir konaklama bu ekrandan rezerve edilemez.")
	case errors.Is(err, application.ErrCancellationTooLate):
		problem(w, r, http.StatusConflict, "accommodation/cancellation-too-late",
			"BOOKING_CANCELLATION_TOO_LATE", "Giriş yapılmış rezervasyon iptal edilemez",
			"Konaklama başladıktan sonra iptal değil, çıkış işlemi yapılır.")
	case errors.Is(err, application.ErrPolicySnapshotMissing):
		problem(w, r, http.StatusConflict, "accommodation/policy-snapshot-missing",
			"POLICY_SNAPSHOT_MISSING", "Rezervasyonun iptal koşulları kayıtlı değil",
			"İptal, rezervasyonun onaylandığı andaki koşullara göre hesaplanır; "+
				"bu rezervasyonda o kayıt yok.")
	case errors.Is(err, application.ErrVoucherRequired):
		problem(w, r, http.StatusUnprocessableEntity, "accommodation/voucher-required",
			"VOUCHER_REQUIRED", "Giriş için kupon kodu gerekli",
			"Misafirin gösterdiği kodu girin.")
	case errors.Is(err, application.ErrNoShowEvidenceRequired):
		problem(w, r, http.StatusConflict, "accommodation/no-show-evidence-required",
			"NO_SHOW_EVIDENCE_REQUIRED", "Gelmedi bildirimi için belge gerekli",
			"Rezervasyona bağlı, taraması temiz en az bir belge olmadan bildirim yapılamaz.")
	case errors.Is(err, application.ErrNoShowTooEarly):
		problem(w, r, http.StatusConflict, "accommodation/no-show-too-early",
			"NO_SHOW_TOO_EARLY", "Giriş saati aralığı henüz kapanmadı",
			"Geç kalan misafir ile gelmeyen misafir aynı şey değildir; aralık kapandıktan "+
				"sonra bildirin.")
	case errors.Is(err, application.ErrNoShowAlreadyReported):
		problem(w, r, http.StatusConflict, "accommodation/no-show-already-reported",
			"NO_SHOW_ALREADY_REPORTED", "Bu rezervasyon için zaten bildirim var",
			"Reddedilen bir bildirim silinmez; her rezervasyon için tek bildirim yapılır.")
	case errors.Is(err, application.ErrNoShowNotFound):
		problem(w, r, http.StatusNotFound, "accommodation/no-show-not-found",
			"NO_SHOW_NOT_FOUND", "Gelmedi bildirimi bulunamadı", "")
	case errors.Is(err, application.ErrNoShowDecided):
		problem(w, r, http.StatusConflict, "accommodation/no-show-decided",
			"NO_SHOW_DECIDED", "Bu bildirim zaten karara bağlandı",
			"Güncel durumu alıp yeniden deneyin.")
	case errors.Is(err, application.ErrNoShowSameActor):
		problem(w, r, http.StatusForbidden, "accommodation/no-show-same-actor",
			"NO_SHOW_SAME_ACTOR", "Bildirimi yapan kişi onu onaylayamaz",
			"Gelmedi bildirimini değerlendiren, bildiren kullanıcıdan farklı olmalı.")
	case errors.Is(err, application.ErrWaitlistEntryNotFound):
		problem(w, r, http.StatusNotFound, "accommodation/waitlist-entry-not-found",
			"WAITLIST_ENTRY_NOT_FOUND", "Bekleme listesi kaydı bulunamadı", "")
	case errors.Is(err, application.ErrWaitlistAlreadyWaiting):
		problem(w, r, http.StatusConflict, "accommodation/waitlist-already-waiting",
			"WAITLIST_ALREADY_WAITING", "Bu tesis ve tarih için zaten bekleme kaydınız var",
			"Aynı kişi, aynı tesis ve aynı giriş tarihi için tek bir açık bekleme kaydı olabilir.")
	case errors.Is(err, application.ErrWaitlistNotOffered):
		problem(w, r, http.StatusConflict, "accommodation/waitlist-not-offered",
			"WAITLIST_NOT_OFFERED", "Bu kayda açık bir teklif yok",
			"Teklifin süresi dolmuş olabilir; sıradaki yerinizi koruyoruz.")
	case errors.Is(err, application.ErrWaitlistTransitionInvalid):
		problem(w, r, http.StatusConflict, "accommodation/waitlist-transition-invalid",
			"WAITLIST_TRANSITION_INVALID", "Bekleme kaydı bu işlem için uygun durumda değil",
			"Kaydın güncel durumunu alıp yeniden deneyin.")
	case errors.Is(err, application.ErrVoucherNotFound):
		// The same answer an unknown booking gets. A refusal that told a real code for
		// another stay apart from one that does not exist would be an oracle a stolen list
		// could be tested against.
		problem(w, r, http.StatusNotFound, "accommodation/voucher-not-found",
			"VOUCHER_NOT_FOUND", "Bu rezervasyona ait böyle bir kupon yok", "")
	case errors.Is(err, application.ErrVoucherAlreadyRedeemed):
		problem(w, r, http.StatusConflict, "accommodation/voucher-already-redeemed",
			"VOUCHER_ALREADY_REDEEMED", "Kupon zaten kullanılmış", "")
	case errors.Is(err, application.ErrVoucherRevoked):
		problem(w, r, http.StatusConflict, "accommodation/voucher-revoked",
			"VOUCHER_REVOKED", "Kupon iptal edilmiş",
			"Rezervasyon sahibine yeni bir kupon düzenlenebilir.")
	case errors.Is(err, application.ErrVoucherExpired):
		problem(w, r, http.StatusConflict, "accommodation/voucher-expired",
			"VOUCHER_EXPIRED", "Kupon geçerlilik süresi dışında", "")
	case errors.Is(err, application.ErrVoucherNotAvailable):
		problem(w, r, http.StatusConflict, "accommodation/voucher-not-available",
			"VOUCHER_NOT_AVAILABLE", "Rezervasyon belgesi henüz oluşturulamaz",
			"Rezervasyon onaylanmadan belge düzenlenemez.")
	case errors.Is(err, application.ErrOccupancyExceeded):
		problem(w, r, http.StatusUnprocessableEntity, "accommodation/occupancy-exceeded",
			"OCCUPANCY_EXCEEDED", "Kişi sayısı bu oda tipine sığmıyor",
			"Yetişkin, çocuk ve toplam kişi sınırlarını aşmayan bir oda tipi seçin.")
	case errors.Is(err, application.ErrEnrollmentNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "accommodation/enrollment-not-found",
			"ENROLLMENT_NOT_FOUND", "Bu tarihlerde geçerli bir plan kaydı yok",
			"Rezervasyon, giriş tarihinde aktif bir plan kaydı üzerinden yapılır.")
	case errors.Is(err, identity.ErrStepUpRequired):
		problem(w, r, http.StatusForbidden, "identity/step-up-required", "STEP_UP_REQUIRED",
			"Bu işlem için parolanızı yeniden doğrulayın",
			"Ödeyeceğiniz tutar kurumun eşiğinin üzerinde; onaydan önce kimliğinizi doğrulayın.")
	case errors.Is(err, application.ErrPropertyNotFound):
		problem(w, r, http.StatusNotFound, "accommodation/property-not-found",
			"PROPERTY_NOT_FOUND", "Tesis bulunamadı", "")
	case errors.Is(err, application.ErrRoomTypeNotFound):
		problem(w, r, http.StatusNotFound, "accommodation/room-type-not-found",
			"ROOM_TYPE_NOT_FOUND", "Oda tipi bulunamadı", "")
	case errors.Is(err, application.ErrPropertyCodeTaken):
		problem(w, r, http.StatusConflict, "accommodation/property-code-taken",
			"PROPERTY_CODE_TAKEN", "Bu kod bu sağlayıcıda zaten kullanılıyor",
			"Tesis kodu sağlayıcı içinde benzersizdir; başka bir kod seçin.")
	case errors.Is(err, application.ErrRoomTypeCodeTaken):
		problem(w, r, http.StatusConflict, "accommodation/room-type-code-taken",
			"ROOM_TYPE_CODE_TAKEN", "Bu kod bu tesiste zaten kullanılıyor",
			"Oda tipi kodu tesis içinde benzersizdir; başka bir kod seçin.")
	case errors.Is(err, application.ErrPropertyScope):
		problem(w, r, http.StatusForbidden, "accommodation/property-scope", "PROPERTY_SCOPE",
			"Bu sağlayıcı adına tesis tanımlayamazsınız",
			"Yalnızca kendi kurumunuzun tesislerini yönetebilirsiniz.")
	case errors.Is(err, application.ErrPersonRequired):
		problem(w, r, http.StatusUnprocessableEntity, "accommodation/person-required",
			"PERSON_REQUIRED", "Sorgu bir hak sahibi adına yapılmalı",
			"Hangi hak sahibi için sorguladığınızı personId ile belirtin.")
	case errors.Is(err, identity.ErrPersonScope):
		httpx.WritePersonScopeProblem(w, r)
	case errors.Is(err, identity.ErrPersonBindingMissing):
		httpx.WritePersonBindingMissingProblem(w, r)
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// An accommodation error carries no personal data: this module's errors name ids,
		// codes, dates and counts, and no name or identifier ever reaches one.
		h.logger.Error("accommodation command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// pathID reads an id from the path. A malformed id is indistinguishable from an unknown one,
// so both are answered with the same not-found: that a hotel exists at all is somebody else's
// business.
func (h *Handler) pathID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// requireIfMatch parses the If-Match header, answering 428 when it is missing.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || n < 1 {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required",
			"IF_MATCH_REQUIRED", "If-Match başlığı gerekli",
			"GET yanıtındaki ETag değerini If-Match olarak gönderin.")
		return 0, false
	}
	return n, true
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

// writeJSON answers with no-store: an availability answer differs by who asked for it, and
// the price in it stops being true when the contract changes.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "geçerli bir kimlik olmalı",
		})
		return nil
	}
	return &id
}

// queryDate reads a required date parameter.
func queryDate(r *http.Request, name string, fields *[]domain.FieldError) time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "REQUIRED", Message: "zorunlu alan",
		})
		return time.Time{}
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı",
		})
		return time.Time{}
	}
	return t
}
