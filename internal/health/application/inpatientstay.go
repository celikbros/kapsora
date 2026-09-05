// The inpatient stay: asking for an admission, extending it, recording where the patient
// actually was, and settling up at discharge (WP-I5-03).
//
// Two properties shape every command below.
//
// **The money is never this package's.** An admission reserves entitlement by being a
// PREAUTHORIZATION request that somebody approves, and the hold is WP-I4-02's. Nothing here
// writes a balance, and the two ports it reaches them through are narrow on purpose: this
// package can raise a request, take a hold, move a hold's end and give a hold back, and
// there is no method on either port that would let it decide anything about entitlement.
//
// **The decision is never this package's either.** A reviewer decides the admission on the
// request page, where they decide every request, and the stay follows by subscribing to the
// outbox event that decision publishes. There is no approve endpoint here and no call into
// WP-I4-01's command: the reviewer does not know a stay exists, and that is what makes the
// screen they already use the only screen they need.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// The ledger reason codes this package's releases are posted under. They are two rather
// than one because a discharge and a cancellation are two different things happening to the
// same hold, and the reason is part of the movement's idempotency key: either of them run
// twice is still one movement, and both of them happening is two.
const (
	ReleaseReasonDischarge = "STAY_DISCHARGE"
	ReleaseReasonCancelled = "STAY_CANCELLED"
)

// The unit an admission is asked for in. A ward night is a night, not a session, and the
// request's line, the authorization's hold and the reconciliation's arithmetic all have to
// agree about that or the figures compare different things.
const admissionUnitType = "NIGHT"

// NewStayInput is the create command.
type NewStayInput struct {
	CaseID                  uuid.UUID
	ProviderOrganizationID  uuid.UUID
	LocationID              *uuid.UUID
	AttendingPractitionerID *uuid.UUID
	AdmissionAt             time.Time
	EstimatedDays           int
	// AdmissionDiagnosisID names a diagnosis of one of this case's own encounters. It is
	// not a diagnosis this package writes: WP-I5-01 owns the diagnosis and the projection
	// that decides who may read one, and a second way to record one would be a second way
	// for a clinical fact to escape.
	AdmissionDiagnosisID *uuid.UUID
}

// StayExtensionInput is the extend command.
type StayExtensionInput struct {
	AdditionalDays int
	ReasonCode     string
	ReasonText     *string
}

// StayFilter is the API-level list request.
type StayFilter struct {
	Cursor                 string
	Limit                  int
	CaseID                 *uuid.UUID
	PersonID               *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	AdmittedFrom           *time.Time
	AdmittedTo             *time.Time
}

// ListStays returns a page of stays with their extensions and segments, each row in the
// projection the caller has earned.
//
// A list never answers 428, for the reason WP-I5-01 wrote down: refusing the whole page over
// one sensitive row would make the list useless, and refusing that one row would say which
// row is sensitive.
func (s *Service) ListStays(ctx context.Context, rc identity.RequestContext, f StayFilter,
	req AccessRequest,
) (StayPage, error) {
	if err := domain.ValidateStayStatusFilter(f.Status); err != nil {
		return StayPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return StayPage{}, err
	}

	var out StayPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		rows, err := s.stayRepo.ListStays(ctx, tx, rc.TenantID, StayQuery{
			Scope: scopeOf(rc), CaseID: f.CaseID, PersonID: f.PersonID,
			ProviderOrganizationID: f.ProviderOrganizationID, Status: f.Status,
			AdmittedFrom: f.AdmittedFrom, AdmittedTo: f.AdmittedTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(stayCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]StayView, 0, len(rows))
		for _, row := range rows {
			view, err := s.viewStay(ctx, tx, rc, row, req, audit.AccessSearch, listRead)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return StayPage{}, err
	}
	return out, nil
}

// GetStay returns one stay with its extensions and segments. This is the read that demands a
// purpose when the case behind it is sensitive.
func (s *Service) GetStay(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req AccessRequest,
) (StayView, error) {
	var (
		out    StayView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.stayRepo.GetStay(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		out, err = s.viewStay(ctx, tx, rc, record, req, audit.AccessView, singleRead)
		return err
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, domain.AggregateStay, id, req)
		return StayView{}, err
	}
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// CreateStay asks for an admission. Everything below happens in one transaction: the window
// check, the duplicate refusal, the preauthorization request the gate decides, and the stay
// row itself. A stay that exists with no request, or a request raised for a stay the unique
// index then refused, is a state no reader ever observes.
//
// The order matters. The request is created before the stay because the stay's foreign key
// needs it; the unique index that refuses a second open stay therefore fires last, and takes
// the request down with it when it does. That is why the duplicate check is the index rather
// than a count taken first: two callers arriving in the same millisecond both pass a count,
// and only one of them passes the index.
func (s *Service) CreateStay(ctx context.Context, rc identity.RequestContext, in NewStayInput,
	req AccessRequest,
) (StayView, error) {
	now := s.now().UTC()
	if err := domain.ValidateNewStay(domain.NewStay{
		AdmissionAt: in.AdmissionAt, EstimatedDays: in.EstimatedDays, Now: now,
	}); err != nil {
		return StayView{}, err
	}
	if in.CaseID == uuid.Nil || in.ProviderOrganizationID == uuid.Nil {
		return StayView{}, fieldError("providerOrganizationId", "REQUIRED",
			"vaka ve sağlayıcı kurumu zorunlu")
	}
	admissionAt := in.AdmissionAt.UTC()
	expectedDischarge := admissionAt.AddDate(0, 0, in.EstimatedDays)

	var out StayView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		if err := s.checkAdmissionWindow(ctx, tx, rc.TenantID, admissionAt, now); err != nil {
			return err
		}
		if err := s.checkProvider(ctx, tx, rc, &in.ProviderOrganizationID); err != nil {
			return err
		}
		episode, err := s.stayRepo.GetStayCase(ctx, tx, rc.TenantID, in.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		if episode.Status == domain.StatusClosed {
			return ErrStayCaseClosed
		}
		if err := s.checkAdmissionDiagnosis(ctx, tx, rc.TenantID, in); err != nil {
			return err
		}
		service, err := s.stayRepo.GetAdmissionService(ctx, tx, rc.TenantID, AdmissionServiceCode)
		if err != nil {
			return err
		}
		if !service.Active {
			return ErrAdmissionServiceUnknown
		}
		// The request the reviewer will decide. It goes through WP-I4-01's own command, so
		// the eligibility evaluation, the rule trace and the document requirement are the
		// ones the request page shows — there is one gate, and this is not a second.
		request, err := s.requests.CreatePreauthorization(ctx, tx, rc, StayRequestInput{
			PersonID: episode.PersonID, EnrollmentID: episode.EnrollmentID,
			ProviderOrganizationID: in.ProviderOrganizationID,
			ServiceDefinitionID:    service.ID, UnitType: admissionUnitType,
			Days: in.EstimatedDays, ServiceDate: admissionAt,
			RequestedStartAt: admissionAt, RequestedEndAt: expectedDischarge,
		})
		if err != nil {
			return err
		}
		record, err := s.stayRepo.CreateStay(ctx, tx, rc.TenantID, NewStayRow{
			CaseID: in.CaseID, ProviderOrganizationID: in.ProviderOrganizationID,
			LocationID: in.LocationID, AttendingPractitionerID: in.AttendingPractitionerID,
			AdmissionAt: admissionAt, EstimatedDays: in.EstimatedDays,
			ExpectedDischargeAt: expectedDischarge, ServiceRequestID: request.ID,
			AdmissionDiagnosisID: in.AdmissionDiagnosisID,
			ActorID:              actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		if err := s.recordStay(ctx, tx, rc, "inpatient_stay.create", record, map[string]any{
			// Ids, codes, counts and statuses only, and never the admission diagnosis:
			// an audit detail is read by everybody who may read audit, and this one is
			// about a person's health.
			"estimated_days": record.EstimatedDays,
			"request_status": request.Status,
			"has_diagnosis":  record.AdmissionDiagnosisID != nil,
			"service_code":   service.Code,
			"provider":       record.ProviderOrganizationID,
		}); err != nil {
			return err
		}
		out, err = s.reloadStay(ctx, tx, rc, record.ID, req)
		return err
	})
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// checkAdmissionWindow refuses an admission dated too far back or too far forward. The
// window is the tenant's: a hospital entering the weekend's admissions on the Monday and one
// booking an operation for next month are both ordinary, and where the line is drawn is a
// tenant's own answer rather than a constant in this file.
func (s *Service) checkAdmissionWindow(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	admissionAt, now time.Time,
) error {
	backdate, err := s.windowSetting(ctx, tx, tenantID, SettingBackdateDays, domain.DefaultBackdateDays)
	if err != nil {
		return err
	}
	future, err := s.windowSetting(ctx, tx, tenantID, SettingFutureDays, domain.DefaultFutureDays)
	if err != nil {
		return err
	}
	if domain.AdmissionInWindow(admissionAt, now, backdate, future) {
		return nil
	}
	return ErrAdmissionOutOfWindow
}

// windowSetting reads one window key, falling back to the documented default. A tenant that
// has configured a nonsense value gets the default too: a window of a hundred thousand days
// is a typo rather than a policy, and honouring it would open the window to any date at all.
func (s *Service) windowSetting(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	key string, fallback int,
) (int, error) {
	value, ok, err := s.stayRepo.WindowSetting(ctx, tx, tenantID, key)
	if err != nil {
		return 0, err
	}
	if !ok || value < 0 || value > domain.MaxWindowDays {
		return fallback, nil
	}
	return value, nil
}

// checkAdmissionDiagnosis keeps the admission diagnosis inside the case it was recorded in.
// A diagnosis borrowed from another case would put another person's condition on this
// admission, and the id alone would never say so.
func (s *Service) checkAdmissionDiagnosis(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in NewStayInput,
) error {
	if in.AdmissionDiagnosisID == nil {
		return nil
	}
	caseID, err := s.stayRepo.GetDiagnosisCase(ctx, tx, tenantID, *in.AdmissionDiagnosisID)
	if err != nil {
		return err
	}
	if caseID != in.CaseID {
		return ErrAdmissionDiagnosisMismatch
	}
	return nil
}

// ExtendStay asks for more days on an admission somebody has already approved.
//
// Two things stop it, and both are refusals rather than something the extension fixes on the
// way past. A stay nobody has decided yet has nothing to extend — asking for more of nothing
// is a second request for the same admission. And an earlier extension nobody has decided is
// the rule of v1.2 10.4 step 6: two extensions in flight are two reviewers reserving
// different numbers of days for the same admission, and whichever landed second would
// silently win.
//
// The service checks the second rule and the partial unique index checks it again. Both exist
// on purpose: the caller is told plainly, and the index refuses it whatever writes it.
func (s *Service) ExtendStay(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in StayExtensionInput, expected int64, req AccessRequest,
) (StayView, error) {
	if err := domain.ValidateStayExtension(domain.NewStayExtension{
		AdditionalDays: in.AdditionalDays, ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
	}); err != nil {
		return StayView{}, err
	}

	var out StayView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.lockStayForCommand(ctx, tx, rc, id, domain.StayCommandExtend, expected)
		if err != nil {
			return err
		}
		pending, err := s.stayRepo.CountPendingExtensions(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if pending > 0 {
			return ErrStayExtensionPending
		}
		sequence, err := s.stayRepo.NextExtensionSequence(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		service, err := s.stayRepo.GetAdmissionService(ctx, tx, rc.TenantID, AdmissionServiceCode)
		if err != nil {
			return err
		}
		episode, err := s.stayRepo.GetStayCase(ctx, tx, rc.TenantID, current.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		// The extension's own PREAUTHORIZATION request. `supersedesRequestId` is empty:
		// an extension does not replace the admission's request, it asks for more beside
		// it, and the extension row is what links the two.
		start := current.ExpectedDischargeAt
		request, err := s.requests.CreatePreauthorization(ctx, tx, rc, StayRequestInput{
			PersonID: episode.PersonID, EnrollmentID: episode.EnrollmentID,
			ProviderOrganizationID: current.ProviderOrganizationID,
			ServiceDefinitionID:    service.ID, UnitType: admissionUnitType,
			Days: in.AdditionalDays, ServiceDate: s.now().UTC(),
			RequestedStartAt: start, RequestedEndAt: start.AddDate(0, 0, in.AdditionalDays),
		})
		if err != nil {
			return err
		}
		extension, err := s.stayRepo.CreateExtension(ctx, tx, rc.TenantID, NewStayExtensionRow{
			StayID: id, SequenceNo: sequence, AdditionalDays: in.AdditionalDays,
			ReasonCode: in.ReasonCode, ReasonText: trimmedPtr(in.ReasonText),
			ServiceRequestID: request.ID, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		// The reason code, never the reason text: the text is clinical and an audit detail
		// is read by everybody who may read audit.
		if err := s.record(ctx, tx, rc, "stay_extension.create", domain.AggregateStayExtension,
			extension.ID, map[string]any{
				"stay":            id,
				"sequence_no":     extension.SequenceNo,
				"additional_days": extension.AdditionalDays,
				"reason_code":     extension.ReasonCode,
				"request_status":  request.Status,
			}); err != nil {
			return err
		}
		out, err = s.reloadStay(ctx, tx, rc, id, req)
		return err
	})
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// PutStaySegments replaces the stay's whole segment set: where the patient was, hour by
// hour. The set is the unit because a segment id is not something anything else hangs off,
// and a diff would be a second way to end up with a gap nobody meant.
//
// Recording where somebody actually is *is* the admission, so the first set on an authorized
// stay moves it to ADMITTED. There is no admit command of its own: a second way to say the
// same thing would be a second thing to keep in step.
func (s *Service) PutStaySegments(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	items []domain.SegmentInput, expected int64, req AccessRequest,
) (StayView, error) {
	var out StayView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.lockStayForCommand(ctx, tx, rc, id, domain.StayCommandSegments, expected)
		if err != nil {
			return err
		}
		if err := domain.ValidateSegments(items, current.AdmissionAt, current.DischargeAt); err != nil {
			return err
		}
		rows := make([]NewSegmentRow, 0, len(items))
		for _, item := range items {
			rows = append(rows, NewSegmentRow{
				StayID: id, SegmentType: item.SegmentType, StartsAt: item.StartsAt.UTC(),
				EndsAt: utcPtr(item.EndsAt), RoomCode: trimmedPtr(item.RoomCode),
				BedCode: trimmedPtr(item.BedCode), ActorID: actorPtr(rc.Principal.ActorID),
			})
		}
		if err := s.stayRepo.ReplaceSegments(ctx, tx, rc.TenantID, id, rows); err != nil {
			return err
		}
		// The status predicate is inside the statement, so a stay already ADMITTED moves
		// nothing and one that is not AUTHORIZED is left where it is.
		admitted, err := s.stayRepo.AdmitStay(ctx, tx, rc.TenantID, id, actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		if err := s.recordStay(ctx, tx, rc, "inpatient_stay.put_segments", current, map[string]any{
			"segments": len(rows), "admitted": admitted,
		}); err != nil {
			return err
		}
		out, err = s.reloadStay(ctx, tx, rc, id, req)
		return err
	})
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// DischargeStay ends an admission and settles up.
//
// Four things happen in one transaction, and they are one fact rather than four: the
// discharge moment is recorded, every segment nobody ended is ended, the days that were
// reserved and never used are released, and the reconciliation is written. A discharge that
// committed without its release would leave a member unable to use a benefit they have
// already paid for; a release that committed without its discharge would give days back for
// an admission still running.
//
// Running it twice releases nothing twice, and that is a property of the statement rather
// than of a flag: the update names the two live statuses and the row version the caller read,
// so the second discharge matches no row — and the ledger key the release is posted under is
// derived from the line and the reason, so even a release that somehow ran again would be
// the same movement rather than a second one.
func (s *Service) DischargeStay(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	dischargeAt time.Time, expected int64, req AccessRequest,
) (StayView, error) {
	now := s.now().UTC()
	if dischargeAt.IsZero() {
		dischargeAt = now
	}
	dischargeAt = dischargeAt.UTC()

	var out StayView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.lockStayForCommand(ctx, tx, rc, id, domain.StayCommandDischarge, expected)
		if err != nil {
			return err
		}
		if err := domain.ValidateDischarge(dischargeAt, current.AdmissionAt, now); err != nil {
			return err
		}
		actual := benefitdomain.MustQuantity(
			fmt.Sprint(domain.ActualDays(current.AdmissionAt, dischargeAt)))
		authorized := parseDays(current.AuthorizedDays)

		// The stay may stand on more than one hold: an approved extension reserves its added
		// days on an authorization of its own, and `authorized_days` counts them all. So the
		// release walks every hold, oldest first, until the unused days are given back —
		// releasing only from the first would leave the extension's days reserved against a
		// bed nobody is in, and the account's totals would look right while they did.
		released, err := s.releaseUnusedDays(ctx, tx, rc, current, authorized.Sub(actual))
		if err != nil {
			return err
		}
		// A stay that ran over what was promised releases nothing and is flagged instead,
		// for the claim to raise as an exception (WP-I5-04). Flagging rather than refusing
		// is deliberate: the person has already had the extra days, and a discharge that
		// could not be recorded would leave the stay open forever.
		over := authorized.IsPositive() && actual.Cmp(authorized) > 0

		if _, err := s.stayRepo.EndOpenSegments(ctx, tx, rc.TenantID, id, dischargeAt,
			actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		if err := s.stayRepo.DischargeStay(ctx, tx, rc.TenantID, id, DischargeRow{
			DischargeAt: dischargeAt, ActualDays: actual.String(), ReleasedDays: released.String(),
			OverAuthorization: over, ActorID: actorPtr(rc.Principal.ActorID), Expected: expected,
		}); err != nil {
			return err
		}
		if err := s.recordStay(ctx, tx, rc, "inpatient_stay.discharge", current, map[string]any{
			"authorized_days": authorized.String(), "actual_days": actual.String(),
			"released_days": released.String(), "over_authorization": over,
		}); err != nil {
			return err
		}
		out, err = s.reloadStay(ctx, tx, rc, id, req)
		return err
	})
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// releaseUnusedDays gives back the days a stay promised and nobody spent, across every
// authorization the stay stands on: its own first, then each approved extension's in the
// order they were granted. It reports what was actually given back, which is what the
// reconciliation records.
//
// ReleaseUnused treats its quantity as a ceiling and returns what it could give back, so a
// hold that has less than is being asked for gives what it has and the rest is asked of the
// next one. Running a discharge twice releases once: the key is the line and the reason, not
// the moment.
func (s *Service) releaseUnusedDays(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	current StayRecord, unused benefitdomain.Quantity,
) (benefitdomain.Quantity, error) {
	released := benefitdomain.ZeroQuantity()
	if !unused.IsPositive() {
		return released, nil
	}
	holds := make([]uuid.UUID, 0, 2)
	if current.AuthorizationID != nil {
		holds = append(holds, *current.AuthorizationID)
	}
	extensions, err := s.stayRepo.ListExtensions(ctx, tx, rc.TenantID, current.ID)
	if err != nil {
		return released, err
	}
	for _, extension := range extensions {
		if extension.Status == domain.ExtensionApproved && extension.AuthorizationID != nil {
			holds = append(holds, *extension.AuthorizationID)
		}
	}

	remaining := unused
	for _, authorizationID := range holds {
		if !remaining.IsPositive() {
			break
		}
		raw, err := s.authorizations.ReleaseUnused(ctx, tx, StayReleaseInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID: authorizationID, Days: remaining.String(),
			ReasonCode: ReleaseReasonDischarge,
		})
		if err != nil {
			return released, err
		}
		gave := parseDays(raw)
		released = released.Add(gave)
		remaining = remaining.Sub(gave)
	}
	return released, nil
}

// CancelStay withdraws an admission nobody has finished. Everything it reserved goes back:
// an admission that did not happen has held nothing, and quietly keeping the hold would be
// the plan charging a member for a bed they never slept in.
func (s *Service) CancelStay(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	reasonCode string, reasonText *string, expected int64, req AccessRequest,
) (StayView, error) {
	if err := domain.ValidateStayReason(reasonCode, reasonText); err != nil {
		return StayView{}, err
	}

	var out StayView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.lockStayForCommand(ctx, tx, rc, id, domain.StayCommandCancel, expected)
		if err != nil {
			return err
		}
		cancelled, err := s.stayRepo.CancelExtensions(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		released := benefitdomain.ZeroQuantity()
		if current.AuthorizationID != nil {
			authorized := parseDays(current.AuthorizedDays)
			if authorized.IsPositive() {
				raw, err := s.authorizations.ReleaseUnused(ctx, tx, StayReleaseInput{
					TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
					AuthorizationID: *current.AuthorizationID, Days: authorized.String(),
					ReasonCode: ReleaseReasonCancelled,
				})
				if err != nil {
					return err
				}
				released = parseDays(raw)
			}
		}
		if err := s.stayRepo.CancelStay(ctx, tx, rc.TenantID, id, reasonCode,
			actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		if err := s.recordStay(ctx, tx, rc, "inpatient_stay.cancel", current, map[string]any{
			"reason_code": reasonCode, "released_days": released.String(),
			"cancelled_extensions": cancelled,
		}); err != nil {
			return err
		}
		out, err = s.reloadStay(ctx, tx, rc, id, req)
		return err
	})
	if err != nil {
		return StayView{}, err
	}
	return out, nil
}

// GetStayReconciliation answers what was promised, what was used, and what was given back.
//
// It refuses a stay that has not been discharged rather than answering with nulls. A
// reconciliation is what the settlement was, and a stay still running has not settled
// anything: an endpoint that answered "authorized 5, used nothing, released nothing" for a
// person currently in a bed would be read as a completed reconciliation by the next thing
// that consumed it.
func (s *Service) GetStayReconciliation(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, req AccessRequest,
) (StayReconciliation, error) {
	var out StayReconciliation
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.stayRepo.GetStay(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if record.Status != domain.StayDischarged || record.DischargeAt == nil {
			return ErrStayNotDischarged
		}
		out = StayReconciliation{
			StayID: record.ID, AuthorizationID: record.AuthorizationID,
			AdmissionAt: record.AdmissionAt, DischargeAt: *record.DischargeAt,
			// The figures are the exact decimal text the numeric columns hold; an absent
			// one is zero here rather than empty, because a reconciliation with a blank
			// in it is not a reconciliation.
			AuthorizedDays:    parseDays(record.AuthorizedDays).String(),
			ActualDays:        parseDays(record.ActualDays).String(),
			ReleasedDays:      parseDays(record.ReleasedDays).String(),
			OverAuthorization: record.OverAuthorization,
		}
		return nil
	})
	if err != nil {
		return StayReconciliation{}, err
	}
	return out, nil
}

// lockStayForCommand is the preamble every stay command shares: read the row under FOR
// UPDATE so two commands on one stay serialise, check the lifecycle allows the command from
// the status the stay is actually in, and check the caller is acting on the version it read.
func (s *Service) lockStayForCommand(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, command string, expected int64,
) (StayRecord, error) {
	current, err := s.stayRepo.LockStay(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return StayRecord{}, err
	}
	if _, ok := domain.StayTarget(command, current.Status); !ok {
		return StayRecord{}, ErrStayTransitionInvalid
	}
	if current.RowVersion != expected {
		return StayRecord{}, ErrVersionMismatch
	}
	return current, nil
}

// viewStay loads a stay's extensions and segments, decides what the caller may see, applies
// the projection and writes the access event a clinical read owes. It is the only path from
// a row to a view: everything that answers with a stay goes through here, so there is one
// place the projection can be removed from and one place a test can prove it is applied.
func (s *Service) viewStay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record StayRecord, req AccessRequest, accessType audit.AccessType, mode readMode,
) (StayView, error) {
	d, err := decide(rc, record.CaseSensitivity, req)
	if err != nil && mode.demandPurpose {
		return StayView{}, err
	}
	extensions, err := s.stayRepo.ListExtensions(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return StayView{}, err
	}
	segments, err := s.stayRepo.ListSegments(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return StayView{}, err
	}
	view := StayView{
		Projection: d.projection,
		Stay:       projectStay(record, d.projection),
		Extensions: projectStayExtensions(extensions, d.projection),
		Segments:   segments,
	}
	switch {
	case d.projection == ProjectionClinical && mode.recordSuccess:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateStay,
			record.ID, accessType, req, audit.OutcomeSuccess); err != nil {
			return StayView{}, err
		}
	case d.refusedSensitive && mode.recordRefusal:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateStay,
			record.ID, accessType, req, audit.OutcomeDenied); err != nil {
			return StayView{}, err
		}
	}
	return view, nil
}

// reloadStay re-reads a stay after a write, so the caller is answered with the row that is
// actually in the database rather than with what the command believed it wrote.
func (s *Service) reloadStay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, req AccessRequest,
) (StayView, error) {
	record, err := s.stayRepo.GetStay(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return StayView{}, err
	}
	return s.viewStay(ctx, tx, rc, record, req, audit.AccessView, commandRead)
}

// recordStay writes one business audit row about a stay. Every detail is an id, a code, a
// count or a status, and never the admission diagnosis, the extension's reason text or
// anything else clinical. audit.SanitizeDetail would drop a badly named key silently, so
// nothing here is named in a way that would make it disappear.
func (s *Service) recordStay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, record StayRecord, extra map[string]any,
) error {
	detail := map[string]any{
		"person": record.PersonID, "case": record.CaseID, "request": record.ServiceRequestID,
		"status": record.Status,
	}
	for k, v := range extra {
		detail[k] = v
	}
	return s.record(ctx, tx, rc, action, domain.AggregateStay, record.ID, detail)
}

// stayCursor is the keyset position of a row on the (admission_at DESC, id DESC) order.
func stayCursor(r StayRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.AdmissionAt, ID: r.ID}
}

// parseDays reads one of the numeric day columns. An empty string is a NULL column and a
// malformed one cannot come out of a numeric column, so both answer zero rather than
// failing a discharge over a value the database itself produced.
func parseDays(raw string) benefitdomain.Quantity {
	if raw == "" {
		return benefitdomain.ZeroQuantity()
	}
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return benefitdomain.ZeroQuantity()
	}
	return value
}
