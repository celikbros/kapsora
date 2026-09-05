package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// referenceAttempts is how many times a create retries a reference collision. The tail is
// forty random bits, so two attempts is already generous; the loop exists so that a
// collision is a retry rather than an error somebody has to read.
const referenceAttempts = 5

// NewRequestInput is the create command.
type NewRequestInput struct {
	RequestType            string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID *uuid.UUID
	ServiceDate            time.Time
	RequestedStartAt       *time.Time
	RequestedEndAt         *time.Time
	Channel                string
	SupersedesRequestID    *uuid.UUID
	Items                  []domain.ItemInput
}

// PatchInput is the merge-patch of a draft header.
type PatchInput struct {
	ProviderOrganizationID    *uuid.UUID
	ClearProviderOrganization bool
	ServiceDate               *time.Time
	RequestedStartAt          *time.Time
	ClearRequestedStartAt     bool
	RequestedEndAt            *time.Time
	ClearRequestedEndAt       bool
	ExpectedVersion           int64
}

// List returns a page of requests with the lines of the version each is currently on.
func (s *Service) List(ctx context.Context, rc identity.RequestContext, f ListFilter) (RequestPage, error) {
	if err := domain.ValidateStatusFilter(f.Status); err != nil {
		return RequestPage{}, err
	}
	if err := domain.ValidateChannelFilter(f.Channel); err != nil {
		return RequestPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return RequestPage{}, err
	}

	var out RequestPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListRequests(ctx, tx, rc.TenantID, RequestQuery{
			Scope: scopeOf(rc), Status: f.Status, PersonID: f.PersonID, ProgramID: f.ProgramID,
			ProviderOrganizationID: f.ProviderOrganizationID, Channel: f.Channel,
			ServiceDateFrom: dayPtr(f.ServiceDateFrom), ServiceDateTo: dayPtr(f.ServiceDateTo),
			CreatedFrom: f.CreatedFrom, CreatedTo: f.CreatedTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			last := rows[pageSize-1]
			out.NextCursor = s.cursors.Encode(cursorOf(last))
			rows = rows[:pageSize]
		}
		out.Items = make([]RequestView, 0, len(rows))
		for _, row := range rows {
			view, err := s.loadView(ctx, tx, rc.TenantID, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return RequestPage{}, err
	}
	return out, nil
}

// Get returns one request with the lines of its current version.
func (s *Service) Get(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (RequestView, error) {
	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		out = view
		return err
	})
	return out, err
}

// Create opens a request in DRAFT with its first version and its lines. Nothing is decided
// here: the gate runs at submit, and a draft is only a form somebody is filling in.
func (s *Service) Create(ctx context.Context, rc identity.RequestContext, in NewRequestInput) (RequestView, error) {
	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.CreateInTx(ctx, tx, rc, in)
		out = view
		return err
	})
	return out, err
}

// CreateInTx is Create inside a transaction the caller already holds.
//
// It exists for one consumer: an inpatient stay (WP-I5-03) is a PREAUTHORIZATION request
// plus a stay row, and the two have to commit together — a stay with no request is a stay
// nobody can decide, and a request raised for a stay that rolled back is work nobody can
// explain. Everything the endpoint does happens here, which is the point: the validation,
// the provider scope, the catalogue check and the status event are one body, so a caller
// coming in this way cannot skip half of them.
func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewRequestInput,
) (RequestView, error) {
	day := domain.DateOnly(in.ServiceDate)
	if err := domain.ValidateNewRequest(domain.NewRequest{
		RequestType: in.RequestType, ServiceDate: in.ServiceDate,
		RequestedStartAt: in.RequestedStartAt, RequestedEndAt: in.RequestedEndAt,
		Channel: in.Channel, Items: in.Items,
	}); err != nil {
		return RequestView{}, err
	}
	if in.PersonID == uuid.Nil || in.EnrollmentID == uuid.Nil {
		return RequestView{}, fieldError("enrollmentId", "REQUIRED",
			"hak sahibi ve plan kaydı zorunlu")
	}
	if err := s.checkProviderScope(rc, in.ProviderOrganizationID); err != nil {
		return RequestView{}, err
	}
	programID, err := s.checkTargets(ctx, tx, rc.TenantID, in, day)
	if err != nil {
		return RequestView{}, err
	}
	// The program is the enrollment's. A caller that named one has just been checked
	// against it; a caller that named none — a provider may read neither programs nor
	// enrollments — gets it from here.
	in.ProgramID = programID
	items, err := s.itemRows(ctx, tx, rc.TenantID, in.RequestType, in.ProviderOrganizationID, in.Items)
	if err != nil {
		return RequestView{}, err
	}

	record, err := s.createWithReference(ctx, tx, rc, in, day)
	if err != nil {
		return RequestView{}, err
	}
	version, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, NewVersionRow{
		ServiceRequestID: record.ID, VersionNo: 1, ActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return RequestView{}, err
	}
	if err := s.repo.ReplaceItems(ctx, tx, rc.TenantID, version.ID, items); err != nil {
		return RequestView{}, err
	}
	if err := s.transition(ctx, tx, rc, record.ID, "", domain.StatusDraft, "CREATE", "", nil,
		map[string]any{"version_no": 1, "item_count": len(items)}); err != nil {
		return RequestView{}, err
	}
	return s.reload(ctx, tx, rc.TenantID, record.ID, scopeOf(rc))
}

// createWithReference retries a reference collision rather than reporting it. The tail is
// random, so a collision means two requests drew the same forty bits on the same day,
// which is worth one more draw and no more explanation than that.
func (s *Service) createWithReference(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewRequestInput, day time.Time,
) (RequestRecord, error) {
	row := NewRequestRow{
		RequestType: in.RequestType, PersonID: in.PersonID, ProgramID: in.ProgramID,
		EnrollmentID: in.EnrollmentID, ProviderOrganizationID: in.ProviderOrganizationID,
		ServiceDate: day, RequestedStartAt: in.RequestedStartAt, RequestedEndAt: in.RequestedEndAt,
		Channel: in.Channel, SupersedesRequestID: in.SupersedesRequestID,
		ActorID: actorPtr(rc.Principal.ActorID),
	}
	for attempt := 0; attempt < referenceAttempts; attempt++ {
		reference, err := newReference(s.now())
		if err != nil {
			return RequestRecord{}, err
		}
		row.Reference = reference
		record, err := s.repo.CreateRequest(ctx, tx, rc.TenantID, row)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrReferenceCollision):
			continue
		default:
			return RequestRecord{}, err
		}
	}
	return RequestRecord{}, ErrReferenceCollision
}

// checkTargets refuses a request naming something this tenant does not have, or naming
// things that do not belong together, before anything is written. It answers the program
// the enrollment belongs to: the one fact a request needs that its caller may be unable
// to read.
func (s *Service) checkTargets(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in NewRequestInput, day time.Time,
) (uuid.UUID, error) {
	enrollment, err := s.repo.GetEnrollment(ctx, tx, tenantID, in.EnrollmentID)
	if err != nil {
		return uuid.Nil, err
	}
	if enrollment.PersonID != in.PersonID {
		return uuid.Nil, ErrEnrollmentMismatch
	}
	if in.ProgramID != uuid.Nil && enrollment.ProgramID != in.ProgramID {
		return uuid.Nil, ErrEnrollmentMismatch
	}
	if !benefitdomain.CoversDate(&enrollment.ValidFrom, enrollment.ValidTo, day) {
		return uuid.Nil, fieldError("serviceDate", "RANGE", "hizmet tarihi plan kaydının geçerlilik aralığı dışında")
	}
	if in.ProviderOrganizationID != nil {
		ok, err := s.repo.ProviderOrganizationExists(ctx, tx, tenantID, *in.ProviderOrganizationID)
		if err != nil {
			return uuid.Nil, err
		}
		if !ok {
			return uuid.Nil, fieldError("providerOrganizationId", "NOT_FOUND", "sağlayıcı kurumu bulunamadı")
		}
	}
	if in.SupersedesRequestID != nil {
		previous, err := s.repo.GetRequest(ctx, tx, tenantID, *in.SupersedesRequestID, Scope{})
		if err != nil {
			if errors.Is(err, ErrRequestNotFound) {
				return uuid.Nil, fieldError("supersedesRequestId", "NOT_FOUND", "önceki talep bulunamadı")
			}
			return uuid.Nil, err
		}
		// Only a refusal is superseded. Naming a live request here would let a second
		// request quietly claim to replace one that is still being worked on.
		if previous.Status != domain.StatusRejected {
			return uuid.Nil, fieldError("supersedesRequestId", "CONFLICT",
				"yalnız reddedilmiş bir talebin yerine yenisi açılabilir")
		}
	}
	return enrollment.ProgramID, nil
}

// itemRows validates the lines against the catalog and numbers them in the order they were
// sent. A definition that needs a provider makes the provider mandatory for the whole
// request, because a request that names none has nobody to deliver it.
func (s *Service) itemRows(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	requestType string, providerID *uuid.UUID, items []domain.ItemInput,
) ([]NewItemRow, error) {
	if err := domain.ValidateItems(items); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	parsed := make([]uuid.UUID, len(items))
	for i, item := range items {
		id, err := uuid.Parse(item.ServiceDefinitionID)
		if err != nil {
			return nil, fieldError(fmt.Sprintf("items[%d].serviceDefinitionId", i), "FORMAT",
				"geçerli bir kimlik olmalı")
		}
		parsed[i] = id
		ids = append(ids, id)
	}
	definitions, err := s.repo.ListServiceDefinitions(ctx, tx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]ServiceDefinitionRecord, len(definitions))
	for _, d := range definitions {
		byID[d.ID] = d
	}

	ve := &domain.ValidationError{}
	needsProvider := domain.Contains(domain.RequiresProviderTypes, requestType)
	rows := make([]NewItemRow, 0, len(items))
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		definition, found := byID[parsed[i]]
		switch {
		case !found:
			ve.Add(path+".serviceDefinitionId", "NOT_FOUND", "hizmet tanımı bulunamadı")
			continue
		case !definition.Active:
			ve.Add(path+".serviceDefinitionId", "INACTIVE", "hizmet tanımı aktif değil")
			continue
		}
		if definition.RequiresProvider {
			needsProvider = true
		}
		quantity, err := benefitdomain.ParseQuantity(item.RequestedQuantity)
		if err != nil {
			ve.Add(path+".requestedQuantity", "FORMAT", "kesin ondalık bir sayı olmalı")
			continue
		}
		row := NewItemRow{
			LineNo: i + 1, ServiceDefinitionID: definition.ID, RequestedQuantity: quantity,
			UnitType: item.UnitType,
		}
		if item.RequestedAmount != "" {
			amount, err := benefitdomain.ParseQuantity(item.RequestedAmount)
			if err != nil {
				ve.Add(path+".requestedAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
				continue
			}
			row.RequestedAmount = &amount
			row.CurrencyCode = optString(item.CurrencyCode)
		}
		rows = append(rows, row)
	}
	if needsProvider && providerID == nil {
		ve.Add("providerOrganizationId", "REQUIRED", "bu talep türü ve hizmet için sağlayıcı zorunlu")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return rows, nil
}

// Patch applies a merge-patch to the header of a DRAFT request. It never writes status:
// the transport refuses a body carrying one before it gets here, and there is no field on
// this input that could carry it either.
func (s *Service) Patch(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	p PatchInput,
) (RequestView, error) {
	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc, id, p.ExpectedVersion)
		if err != nil {
			return err
		}
		next := DraftHeaderRow{
			ProviderOrganizationID: current.ProviderOrganizationID,
			ServiceDate:            current.ServiceDate,
			RequestedStartAt:       current.RequestedStartAt,
			RequestedEndAt:         current.RequestedEndAt,
			ActorID:                actorPtr(rc.Principal.ActorID),
		}
		switch {
		case p.ClearProviderOrganization:
			next.ProviderOrganizationID = nil
		case p.ProviderOrganizationID != nil:
			next.ProviderOrganizationID = p.ProviderOrganizationID
		}
		if p.ServiceDate != nil {
			next.ServiceDate = domain.DateOnly(*p.ServiceDate)
		}
		switch {
		case p.ClearRequestedStartAt:
			next.RequestedStartAt = nil
		case p.RequestedStartAt != nil:
			next.RequestedStartAt = p.RequestedStartAt
		}
		switch {
		case p.ClearRequestedEndAt:
			next.RequestedEndAt = nil
		case p.RequestedEndAt != nil:
			next.RequestedEndAt = p.RequestedEndAt
		}
		if err := domain.ValidatePatch(next.RequestedStartAt, next.RequestedEndAt); err != nil {
			return err
		}
		if err := s.checkProviderScope(rc, next.ProviderOrganizationID); err != nil {
			return err
		}
		if next.ProviderOrganizationID != nil {
			ok, err := s.repo.ProviderOrganizationExists(ctx, tx, rc.TenantID, *next.ProviderOrganizationID)
			if err != nil {
				return err
			}
			if !ok {
				return fieldError("providerOrganizationId", "NOT_FOUND", "sağlayıcı kurumu bulunamadı")
			}
		}
		if err := s.repo.UpdateDraftHeader(ctx, tx, rc.TenantID, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "service_request.update", id, map[string]any{
			"version_no": current.CurrentVersionNo,
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// ReplaceItems replaces the whole line set of the DRAFT version.
func (s *Service) ReplaceItems(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	items []domain.ItemInput, expected int64,
) (RequestView, error) {
	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc, id, expected)
		if err != nil {
			return err
		}
		version, err := s.repo.GetDraftVersion(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		rows, err := s.itemRows(ctx, tx, rc.TenantID, current.RequestType,
			current.ProviderOrganizationID, items)
		if err != nil {
			return err
		}
		if err := s.repo.ReplaceItems(ctx, tx, rc.TenantID, version.ID, rows); err != nil {
			return err
		}
		// The lines belong to the version, so nothing on the request row changed; the ETag
		// still has to move, or a caller holding it would think its copy is current.
		if err := s.repo.TouchRequest(ctx, tx, rc.TenantID, id, actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "service_request.items_replace", id, map[string]any{
			"version_no": version.VersionNo, "item_count": len(rows),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// lockDraft takes the request FOR UPDATE and refuses anything that is not an editable
// draft at the version the caller holds.
func (s *Service) lockDraft(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, expected int64,
) (RequestRecord, error) {
	current, err := s.repo.LockRequest(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return RequestRecord{}, err
	}
	if !domain.Editable(current.Status) {
		// Everything that is not a draft has a frozen version behind it, and the answer a
		// caller needs is "what you are editing was submitted", not "wrong state".
		return RequestRecord{}, ErrVersionImmutable
	}
	if current.RowVersion != expected {
		return RequestRecord{}, ErrVersionMismatch
	}
	return current, nil
}

// checkProviderScope enforces the provider boundary on a write. A provider-scoped actor
// may only raise and edit requests naming one of its own organizations; a tenant-wide
// actor has no such grant and is unaffected.
func (s *Service) checkProviderScope(rc identity.RequestContext, providerID *uuid.UUID) error {
	scope := scopeOf(rc)
	if !scope.Restricted() {
		return nil
	}
	if providerID == nil {
		return ErrProviderScope
	}
	for _, id := range scope.OrganizationIDs {
		if id == *providerID {
			return nil
		}
	}
	return ErrProviderScope
}

// GetVersion returns one version of a request with the lines it carried.
func (s *Service) GetVersion(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	versionNo int,
) (VersionView, error) {
	var out VersionView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRequest(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		version, err := s.repo.GetVersionByNo(ctx, tx, rc.TenantID, id, versionNo)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		out = VersionView{Version: version, Items: items}
		return nil
	})
	return out, err
}

// ListVersions returns every version of a request, newest first.
func (s *Service) ListVersions(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) ([]VersionRecord, error) {
	var out []VersionRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRequest(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		versions, err := s.repo.ListVersions(ctx, tx, rc.TenantID, id)
		out = versions
		return err
	})
	return out, err
}

func dayPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := domain.DateOnly(*t)
	return &d
}
