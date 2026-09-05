package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewReportInput is the create command. `SupersedesReportID` is what makes it a correction:
// with it the new report is version n+1 of an existing chain and inherits the reference, the
// root and — unless the caller says otherwise — the header and the lines of the version it
// corrects.
type NewReportInput struct {
	PersonID                      uuid.UUID
	CaseID                        *uuid.UUID
	SupersedesReportID            *uuid.UUID
	ReportType                    string
	ReportSubtype                 *string
	IssuingPractitionerID         *uuid.UUID
	IssuingProviderOrganizationID *uuid.UUID
	IssuedAt                      *time.Time
	ValidFrom                     *time.Time
	ValidTo                       *time.Time
	ClinicalSummary               *string
}

// ReportFilter is the API-level list request.
type ReportFilter struct {
	Cursor                 string
	Limit                  int
	PersonID               *uuid.UUID
	CaseID                 *uuid.UUID
	RootReportID           *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	ReportType             string
	ValidOn                *time.Time
}

// ListReports returns a page of reports, each row in the projection the caller has earned.
//
// Like ListCases it never answers 428: refusing a whole page over one report attached to a
// sensitive case would make the list useless, and refusing that one row would say which row
// it is. The single read is where the purpose is demanded.
func (s *Service) ListReports(ctx context.Context, rc identity.RequestContext, f ReportFilter,
	req AccessRequest,
) (ReportPage, error) {
	if err := domain.ValidateReportStatusFilter(f.Status); err != nil {
		return ReportPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ReportPage{}, err
	}

	var out ReportPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		rows, err := s.reports.ListReports(ctx, tx, rc.TenantID, ReportQuery{
			Scope: scopeOf(rc), PersonID: f.PersonID, CaseID: f.CaseID,
			RootReportID: f.RootReportID, ProviderOrganizationID: f.ProviderOrganizationID,
			Status: f.Status, ReportType: f.ReportType, ValidOn: f.ValidOn,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(reportCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]ReportView, 0, len(rows))
		for _, row := range rows {
			view, err := s.viewReport(ctx, tx, rc, row, req, audit.AccessSearch, listRead)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return ReportPage{}, err
	}
	return out, nil
}

// GetReport returns one report with its lines and attachments. This is the read that demands
// a purpose when the report hangs off a sensitive case, and the refusal is recorded.
func (s *Service) GetReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req AccessRequest,
) (ReportView, error) {
	var (
		out    ReportView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.reports.GetReport(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		out, err = s.viewReport(ctx, tx, rc, record, req, audit.AccessView, singleRead)
		return err
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, domain.AggregateMedicalReport, id, audit.AccessView, req)
		return ReportView{}, err
	}
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// ListUsages answers "which claim leaned on this report, and when". It is the trace v1.2
// 10.5 step 6 asks for and it carries nothing clinical at all — a usage row is three ids and
// a moment — so it is served whole to anybody who may read the report.
func (s *Service) ListUsages(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	cursor string, limit int,
) (UsagePage, error) {
	after, pageSize, err := s.paging(cursor, limit)
	if err != nil {
		return UsagePage{}, err
	}
	var out UsagePage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// Through the caller's own boundary first, so a report it cannot see has no usage
		// list either.
		if _, err := s.reports.GetReport(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		rows, err := s.reports.ListReportUsages(ctx, tx, rc.TenantID, UsageQuery{
			ReportID: id, After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(usageCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return UsagePage{}, err
	}
	return out, nil
}

// viewReport loads a report's lines and attachments, decides what the caller may see,
// applies the projection and writes the access event a clinical read owes. It is the only
// path from a row to a view: everything that answers with a report goes through here, so
// there is one place the projection can be removed from and one place a test can prove it
// is applied.
//
// It asks `decide` — the same function every case read asks, in projection.go — with the
// sensitivity of the case the report hangs off. A report is exactly as sensitive as the
// episode of care it belongs to, and a second rule about that would be a second place for
// the two to disagree.
func (s *Service) viewReport(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ReportRecord, req AccessRequest, accessType audit.AccessType, mode readMode,
) (ReportView, error) {
	d, err := decide(rc, record.CaseSensitivity, req)
	if err != nil && mode.demandPurpose {
		return ReportView{}, err
	}
	services, err := s.reports.ListReportServices(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return ReportView{}, err
	}
	var documents []ReportDocumentRecord
	if d.projection == ProjectionClinical {
		// Only the clinical projection reads them at all: a query nobody uses the answer of
		// is a query that can start being used by accident.
		documents, err = s.reports.ListReportDocuments(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return ReportView{}, err
		}
	}
	view := ReportView{
		Projection: d.projection,
		Report:     projectReport(record, d.projection),
		Services:   projectReportServices(services, d.projection),
		Documents:  projectReportDocuments(documents, d.projection),
	}
	switch {
	case d.projection == ProjectionClinical && mode.recordSuccess:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateMedicalReport,
			record.ID, accessType, req, audit.OutcomeSuccess); err != nil {
			return ReportView{}, err
		}
	case d.refusedSensitive && mode.recordRefusal:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateMedicalReport,
			record.ID, accessType, req, audit.OutcomeDenied); err != nil {
			return ReportView{}, err
		}
	}
	return view, nil
}

// CreateReport writes a draft report, either the first version of a new chain or a
// correction of a decided one.
//
// A correction is the only way an approved report ever changes, and it changes nothing: the
// version it supersedes keeps its decision, its lines and its usage rows exactly as they
// are, and the new version starts as a draft that has to be submitted and reviewed like any
// other. What it inherits is the reference — so a member quoting "MR-2026…" is still quoting
// the same report — the chain root, and, unless the caller says otherwise, the header and
// the lines it is correcting.
func (s *Service) CreateReport(ctx context.Context, rc identity.RequestContext,
	in NewReportInput,
) (ReportView, error) {
	now := s.now().UTC()

	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		row := NewReportRow{
			PersonID: in.PersonID, CaseID: in.CaseID,
			ReportType: in.ReportType, ReportSubtype: trimmedPtr(in.ReportSubtype),
			IssuingPractitionerID:         in.IssuingPractitionerID,
			IssuingProviderOrganizationID: in.IssuingProviderOrganizationID,
			ClinicalSummary:               trimmedPtr(in.ClinicalSummary),
			VersionNo:                     1,
			ActorID:                       actorPtr(rc.Principal.ActorID),
		}
		issuedAt, validFrom, validTo := in.IssuedAt, in.ValidFrom, in.ValidTo

		var copied []NewReportServiceRow
		if in.SupersedesReportID != nil {
			previous, err := s.reports.LockReport(ctx, tx, rc.TenantID, *in.SupersedesReportID, scopeOf(rc))
			if err != nil {
				if errors.Is(err, ErrReportNotFound) {
					return ErrReportSupersedesInvalid
				}
				return err
			}
			if err := s.checkSupersedable(ctx, tx, rc, previous, in); err != nil {
				return err
			}
			inheritReport(&row, &issuedAt, &validFrom, &validTo, previous)
			copied, err = s.copyReportServices(ctx, tx, rc, previous.ID)
			if err != nil {
				return err
			}
		}

		if err := domain.ValidateNewReport(domain.NewReport{
			ReportType: row.ReportType, ReportSubtype: row.ReportSubtype,
			IssuedAt: issuedAt, ValidFrom: validFrom, ValidTo: validTo,
			Summary: row.ClinicalSummary, Correction: in.SupersedesReportID != nil, Now: now,
		}); err != nil {
			return err
		}
		if row.PersonID == uuid.Nil {
			return fieldError("personId", "REQUIRED", "hak sahibi zorunlu")
		}
		row.IssuedAt, row.ValidFrom, row.ValidTo = dayOf(issuedAt), dayOf(validFrom), dayOf(validTo)

		if err := s.checkProvider(ctx, tx, rc, row.IssuingProviderOrganizationID); err != nil {
			return err
		}
		if err := s.checkReportCase(ctx, tx, rc, row.PersonID, row.CaseID); err != nil {
			return err
		}

		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("health: report id: %w", err)
		}
		row.ID = id
		if row.VersionNo == 1 {
			// Version 1 is its own chain root, which is what lets a single partial index
			// rather than a recursive query answer "does this chain already have an
			// approved version".
			row.RootReportID = id
			reference, err := newReportReference(now)
			if err != nil {
				return err
			}
			row.Reference = reference
		}

		record, err := s.reports.CreateReport(ctx, tx, rc.TenantID, row)
		if err != nil {
			return err
		}
		if len(copied) > 0 {
			if err := s.reports.ReplaceReportServices(ctx, tx, rc.TenantID, record.ID, copied); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "medical_report.create", domain.AggregateMedicalReport,
			record.ID, map[string]any{
				// Ids, codes, counts and booleans only. Not the report type, not the
				// subtype and not a word of the summary: an audit detail is read by
				// everybody who may read audit, and this one is about a person's health.
				"reference":    record.Reference,
				"version_no":   record.VersionNo,
				"status":       record.Status,
				"person":       record.PersonID,
				"root":         record.RootReportID,
				"correction":   record.SupersedesReportID != nil,
				"has_case":     record.CaseID != nil,
				"has_provider": record.IssuingProviderOrganizationID != nil,
				"lines":        len(copied),
			}); err != nil {
			return err
		}
		out, err = s.viewReport(ctx, tx, rc, record, AccessRequest{}, audit.AccessView, commandRead)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// checkSupersedable is section 2.3's precondition: only a decided version may be corrected,
// only by a version of the same chain, only for the same person, and only once.
func (s *Service) checkSupersedable(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	previous ReportRecord, in NewReportInput,
) error {
	if !domain.ReportDecided(previous.Status) {
		// A report nobody has decided is corrected by editing it. Producing a second
		// version of a draft would leave two drafts of one report and no way to say which
		// one a provider meant to submit.
		return ErrReportSupersedesInvalid
	}
	if in.PersonID != uuid.Nil && in.PersonID != previous.PersonID {
		return ErrReportSupersedesInvalid
	}
	chain, err := s.reports.ListReportChain(ctx, tx, rc.TenantID, previous.RootReportID)
	if err != nil {
		return err
	}
	for _, version := range chain {
		// Only the newest version of a chain may be corrected. Correcting version 1 while
		// version 2 exists would fork the chain, and "which one is in force" would stop
		// having an answer.
		if version.VersionNo > previous.VersionNo {
			return ErrReportSupersedesInvalid
		}
	}
	return nil
}

// inheritReport fills the correction's chain fields and lets it stand on the header of the
// version it corrects wherever the caller sent nothing. A correction that had to repeat
// every field would be a correction that quietly loses one.
func inheritReport(row *NewReportRow, issuedAt, validFrom, validTo **time.Time, previous ReportRecord) {
	row.PersonID = previous.PersonID
	row.Reference = previous.Reference
	row.RootReportID = previous.RootReportID
	row.VersionNo = previous.VersionNo + 1
	id := previous.ID
	row.SupersedesReportID = &id
	if row.CaseID == nil {
		row.CaseID = previous.CaseID
	}
	if row.ReportType == "" {
		row.ReportType = previous.ReportType
	}
	if row.ReportSubtype == nil {
		row.ReportSubtype = previous.ReportSubtype
	}
	if row.IssuingPractitionerID == nil {
		row.IssuingPractitionerID = previous.IssuingPractitionerID
	}
	if row.IssuingProviderOrganizationID == nil {
		row.IssuingProviderOrganizationID = previous.IssuingProviderOrganizationID
	}
	if row.ClinicalSummary == nil {
		row.ClinicalSummary = previous.ClinicalSummary
	}
	if *issuedAt == nil {
		at := previous.IssuedAt
		*issuedAt = &at
	}
	if *validFrom == nil {
		at := previous.ValidFrom
		*validFrom = &at
	}
	if *validTo == nil {
		at := previous.ValidTo
		*validTo = &at
	}
}

// copyReportServices reads the lines of the version being corrected, as insert payloads for
// the new one. The ids are not carried over: a line is part of the version that holds it,
// and a claim that quoted a line id of version 1 quoted version 1.
func (s *Service) copyReportServices(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	reportID uuid.UUID,
) ([]NewReportServiceRow, error) {
	lines, err := s.reports.ListReportServices(ctx, tx, rc.TenantID, reportID)
	if err != nil {
		return nil, err
	}
	out := make([]NewReportServiceRow, 0, len(lines))
	for _, line := range lines {
		out = append(out, NewReportServiceRow{
			ServiceDefinitionID: line.ServiceDefinitionID, CoveredQuantity: line.CoveredQuantity,
			CoveredAmount: line.CoveredAmount, CurrencyCode: line.CurrencyCode,
			Notes: line.Notes, ActorID: actorPtr(rc.Principal.ActorID),
		})
	}
	return out, nil
}

// PatchReportDraft rewrites a draft's header. Everything that has left DRAFT answers
// ErrReportImmutable, which is the freeze the whole package exists for: an approved report
// is what the reviewer saw, and a rejected one is what the provider was told about.
func (s *Service) PatchReportDraft(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, in NewReportInput, expected int64,
) (ReportView, error) {
	now := s.now().UTC()
	if err := domain.ValidateNewReport(domain.NewReport{
		ReportType: in.ReportType, ReportSubtype: in.ReportSubtype, IssuedAt: in.IssuedAt,
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Summary: in.ClinicalSummary, Now: now,
	}); err != nil {
		return ReportView{}, err
	}

	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.reports.LockReport(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if domain.ReportFrozen(current.Status) {
			return ErrReportImmutable
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if err := s.checkProvider(ctx, tx, rc, in.IssuingProviderOrganizationID); err != nil {
			return err
		}
		if err := s.checkReportCase(ctx, tx, rc, current.PersonID, in.CaseID); err != nil {
			return err
		}
		if err := s.reports.PatchReportDraft(ctx, tx, rc.TenantID, id, PatchReportRow{
			CaseID: in.CaseID, ReportType: in.ReportType, ReportSubtype: trimmedPtr(in.ReportSubtype),
			IssuingPractitionerID:         in.IssuingPractitionerID,
			IssuingProviderOrganizationID: in.IssuingProviderOrganizationID,
			IssuedAt:                      dayOf(in.IssuedAt), ValidFrom: dayOf(in.ValidFrom), ValidTo: dayOf(in.ValidTo),
			ClinicalSummary: trimmedPtr(in.ClinicalSummary),
			ActorID:         actorPtr(rc.Principal.ActorID),
		}, expected); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "medical_report.update", domain.AggregateMedicalReport, id,
			map[string]any{
				"reference": current.Reference, "version_no": current.VersionNo,
				"person": current.PersonID, "has_case": in.CaseID != nil,
			}); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// PutReportServices replaces the report's service set as a whole: the set is the unit, and a
// line id is not something anything else hangs off. A report that has left DRAFT answers
// ErrReportImmutable here for the same reason it does above — the lines are what the report
// says, and rewriting them would rewrite an approved report without touching its header.
func (s *Service) PutReportServices(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, items []domain.ReportServiceInput, expected int64,
) (ReportView, error) {
	if err := domain.ValidateReportServices(items); err != nil {
		return ReportView{}, err
	}

	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.reports.LockReport(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if domain.ReportFrozen(current.Status) {
			return ErrReportImmutable
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		rows := make([]NewReportServiceRow, 0, len(items))
		for _, item := range items {
			definitionID, err := uuid.Parse(item.ServiceDefinitionID)
			if err != nil {
				return ErrReportServiceUnknown
			}
			if _, err := s.reports.GetReportServiceDefinition(ctx, tx, rc.TenantID, definitionID); err != nil {
				return err
			}
			rows = append(rows, NewReportServiceRow{
				ServiceDefinitionID: definitionID,
				CoveredQuantity:     trimmedPtr(item.CoveredQuantity),
				CoveredAmount:       trimmedPtr(item.CoveredAmount),
				CurrencyCode:        trimmedPtr(item.CurrencyCode),
				Notes:               trimmedPtr(item.Notes),
				ActorID:             actorPtr(rc.Principal.ActorID),
			})
		}
		if err := s.reports.ReplaceReportServices(ctx, tx, rc.TenantID, id, rows); err != nil {
			return err
		}
		// The lines are part of what the report says, so replacing them moves the ETag. A
		// caller still holding the old one is holding a report that no longer says what it
		// said, and the next command it gives should be refused.
		if err := s.reports.TouchReportDraft(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "medical_report.services.put", domain.AggregateMedicalReport,
			id, map[string]any{
				"reference": current.Reference, "version_no": current.VersionNo,
				"person": current.PersonID, "lines": len(rows),
			}); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// reloadReport reads the report back after a write and answers it in the projection the
// caller has earned, recording nothing: the access log answers who *looked* at a person's
// clinical data, and the person who just wrote it is on the business audit row instead.
func (s *Service) reloadReport(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID,
) (ReportView, error) {
	record, err := s.reports.GetReport(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return ReportView{}, err
	}
	return s.viewReport(ctx, tx, rc, record, AccessRequest{}, audit.AccessView, commandRead)
}

// checkReportCase keeps a report and the case it hangs off about the same person. A report
// filed under somebody else's case is a clinical record on the wrong chart.
func (s *Service) checkReportCase(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID uuid.UUID, caseID *uuid.UUID,
) error {
	if caseID == nil {
		return nil
	}
	record, err := s.reports.GetReportCase(ctx, tx, rc.TenantID, *caseID)
	if err != nil {
		return err
	}
	if record.PersonID != personID {
		return ErrReportCaseMismatch
	}
	return nil
}

// reportCursor is the keyset position of a row on the (created_at DESC, id DESC) order.
func reportCursor(r ReportRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

// usageCursor is the keyset position of a usage row.
func usageCursor(r ReportUsageRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.UsedAt, ID: r.ID}
}

// dayOf drops the time of day. Every date column of a report is a date, and a report valid
// "from the fourth at 14:32" is a report nobody could explain to the person holding it.
func dayOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// referenceEncoding is uppercase letters and digits only, which is what somebody has to read
// out over a telephone. It is the same alphabet a request reference uses.
var referenceEncoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// newReportReference builds a report reference of the form MR-20260904-XXXXXXXX. The random
// tail rather than a counter is deliberate and is the same choice WP-I4-01 made: a per-tenant
// counter would leak how many reports a provider is writing to anybody who can create two
// and subtract.
//
// The reference names the chain rather than the version: a correction inherits it, and
// (reference, version_no) is what identifies a version. A member quoting "MR-2026…" over the
// telephone is quoting the report, and asking them for a version number as well would be
// asking them to know something the product has never shown them.
func newReportReference(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("health: generate report reference: %w", err)
	}
	return fmt.Sprintf("MR-%s-%s", now.UTC().Format("20060102"), referenceEncoding.EncodeToString(buf)), nil
}
