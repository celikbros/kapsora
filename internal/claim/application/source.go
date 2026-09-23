package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Source errors deliberately disclose no clinical reason to financial callers.
var (
	ErrSourceNotFound       = errors.New("claim: source not found")
	ErrSourceNotReady       = errors.New("claim: source is incomplete or ambiguous")
	ErrSourceAlreadyClaimed = errors.New("claim: case already claimed")
)

// CaseSourceSummary is the financial identity of an episode. No clinical fields.
type CaseSourceSummary struct {
	ID                                  uuid.UUID
	OpenedAt, ServiceDate               time.Time
	RowVersion                          int64
	RequestReference, PersonDisplayName string
}

// CaseSource stays inside the application boundary. Transport uses an explicit allowlist.
type CaseSource struct {
	CaseSourceSummary
	PersonID, ProgramID, EnrollmentID, ProviderID uuid.UUID
	AuthorizationIDs, DiagnosisIDs                []uuid.UUID
	Ongoing, AlreadyRaised                        bool
	Lines                                         []CaseSourceLine
}

// CaseSourceLine includes internal matching evidence, never sent to billing.
type CaseSourceLine struct {
	ServiceID                      uuid.UUID
	Code, Name, UnitType, Quantity string
	ReportIDs                      []uuid.UUID
}

// CaseSourceQuery pages within the caller's provider boundary.
type CaseSourceQuery struct {
	Scope    Scope
	After    *httpx.Cursor
	Now      time.Time
	PageSize int
}

// CaseCharge is a financial entry; its clinical associations are resolved by the server.
type CaseCharge struct {
	ServiceID            uuid.UUID
	Quantity, LineAmount string
}

// ListCaseSources returns candidates. Detail/create recheck the full handoff.
func (s *Service) ListCaseSources(ctx context.Context, rc identity.RequestContext, cursor string, limit int) ([]CaseSourceSummary, string, error) {
	if !rc.Has(PermissionCreate) {
		return nil, "", ErrProviderScope
	}
	after, size, err := s.paging(cursor, limit)
	if err != nil {
		return nil, "", err
	}
	var rows []CaseSourceSummary
	var next string
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListCaseSources(ctx, tx, rc.TenantID, CaseSourceQuery{Scope: scopeOf(rc), After: after, Now: s.now(), PageSize: size + 1})
		if err != nil {
			return err
		}
		if len(rows) > size {
			last := rows[size-1]
			next = s.cursors.Encode(httpx.Cursor{CreatedAt: last.OpenedAt, ID: last.ID})
			rows = rows[:size]
		}
		return nil
	})
	return rows, next, err
}

// GetCaseSource resolves only an unambiguous, completed outpatient episode.
func (s *Service) GetCaseSource(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (CaseSource, error) {
	if !rc.Has(PermissionCreate) {
		return CaseSource{}, ErrProviderScope
	}
	var out CaseSource
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.readySource(ctx, tx, rc, id)
		return err
	})
	return out, err
}

func (s *Service) readySource(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, id uuid.UUID) (CaseSource, error) {
	source, err := s.repo.LockCaseSource(ctx, tx, rc.TenantID, id, scopeOf(rc), s.now())
	if err != nil {
		return CaseSource{}, err
	}
	if source.AlreadyRaised {
		return CaseSource{}, ErrSourceAlreadyClaimed
	}
	if source.Ongoing || len(source.AuthorizationIDs) != 1 || len(source.DiagnosisIDs) != 1 {
		return CaseSource{}, ErrSourceNotReady
	}
	source.Lines, err = s.repo.CaseSourceLines(ctx, tx, rc.TenantID, source)
	if err != nil {
		return CaseSource{}, err
	}
	if len(source.Lines) == 0 {
		return CaseSource{}, ErrSourceNotReady
	}
	seen := map[uuid.UUID]bool{}
	for _, line := range source.Lines {
		if len(line.ReportIDs) != 1 || seen[line.ServiceID] {
			return CaseSource{}, ErrSourceNotReady
		}
		seen[line.ServiceID] = true
	}
	return source, nil
}

// CreateFromCase serializes handoffs on the case row, retaining every association in
// the same transaction as the draft. It grants no clinical read to its financial caller.
func (s *Service) CreateFromCase(ctx context.Context, rc identity.RequestContext, id uuid.UUID, expected int64, charges []CaseCharge) (ClaimView, error) {
	if !rc.Has(PermissionCreate) {
		return ClaimView{}, ErrProviderScope
	}
	if len(charges) == 0 || len(charges) > 100 {
		return ClaimView{}, ErrLineRequired
	}
	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		source, err := s.readySource(ctx, tx, rc, id)
		if err != nil {
			return err
		}
		if source.RowVersion != expected {
			return ErrVersionMismatch
		}
		in := NewClaimInput{PersonID: source.PersonID, ProgramID: source.ProgramID, EnrollmentID: source.EnrollmentID,
			ProviderOrganizationID: source.ProviderID, CaseID: &id, AuthorizationID: &source.AuthorizationIDs[0],
			ServiceDateFrom: source.ServiceDate, ServiceDateTo: source.ServiceDate, Channel: domain.DefaultChannel}
		available := map[uuid.UUID]CaseSourceLine{}
		for _, line := range source.Lines {
			available[line.ServiceID] = line
		}
		for i, charge := range charges {
			line, ok := available[charge.ServiceID]
			if !ok {
				return fieldError("lines", "SOURCE_SERVICE", "Vakanın hizmetlerinden birini seçin.")
			}
			delete(available, charge.ServiceID)
			qty, err := benefitdomain.ParseQuantity(charge.Quantity)
			maxQty, maxErr := benefitdomain.ParseQuantity(line.Quantity)
			if err != nil || maxErr != nil || qty.Cmp(maxQty) > 0 {
				return fieldError("lines", "SOURCE_QUANTITY", "Miktar ayrılan hakkı aşamaz.")
			}
			currency := "TRY"
			in.Lines = append(in.Lines, NewLineInput{LineNo: i + 1, ServiceDefinitionID: line.ServiceID, UnitType: line.UnitType,
				Quantity: charge.Quantity, LineAmount: charge.LineAmount, CurrencyCode: &currency,
				DiagnosisID: &source.DiagnosisIDs[0], MedicalReportID: &line.ReportIDs[0]})
		}
		if err := validateLineInputs(in.Lines); err != nil {
			return err
		}
		if err := s.checkProvider(ctx, tx, rc.TenantID, in.ProviderOrganizationID); err != nil {
			return err
		}
		if err := s.checkEnrollment(ctx, tx, rc.TenantID, in); err != nil {
			return err
		}
		record, err := s.createDraft(ctx, tx, rc, in, in.Channel)
		if err != nil {
			return err
		}
		out, err = s.viewClaim(ctx, tx, rc.TenantID, record, ProjectionFinancial)
		return err
	})
	return out, err
}
