package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/health/domain"
)

// ReportCoverage is the port WP-I5-04 calls: may this claim lean on this report for this
// service on this day (section 2.4)?
//
// Three things have to be true and each of them is a different refusal, because a caller
// that is told "no" deserves to be told which of the three it was: the report is APPROVED,
// the service date lies inside [valid_from, valid_to], and the service is one the report
// names. A version a later one superseded is not approved, so a claim can never lean on a
// report the reviewer has since replaced — while a claim that already leaned on it keeps its
// usage row saying so.
//
// A usable answer writes a usage row. That is the whole trace v1.2 10.5 step 6 asks for: the
// question "prove the claim you settled was covered by a report a doctor signed" is answered
// by a row naming the version, the thing that used it and the moment. A refusal writes
// nothing — nothing used a report it was not allowed to use.
//
// It runs inside the caller's transaction and opens none of its own, so a claim that rolls
// back has used nothing.
//
// Nothing clinical leaves this function. The answer carries a reference, a version number,
// the covered limits and a reason code, and neither the summary, nor the report type, nor
// the line's note ever reaches it — so there is no access event to write and no projection
// to apply, and a caller holding no clinical grant may still settle a claim.
func (s *Service) ReportCoverage(ctx context.Context, tx pgx.Tx, in CoverageRequest) (Coverage, error) {
	if err := domain.ValidateReportUsedByType(in.UsedByType); err != nil {
		return Coverage{}, err
	}
	row, err := s.reports.ReportCoverageRow(ctx, tx, in.TenantID, in.ReportID, in.ServiceDefinitionID)
	if err != nil {
		return Coverage{}, err
	}

	out := Coverage{ReportID: row.ReportID, Reference: row.Reference, VersionNo: row.VersionNo}
	day := dayOfValue(in.ServiceDate)
	switch {
	case row.Status != domain.ReportStatusApproved:
		out.ReasonCode = CoverageNotApproved
	case day.Before(dayOfValue(row.ValidFrom)) || day.After(dayOfValue(row.ValidTo)):
		// Both bounds are inclusive: a report valid "from the first to the thirtieth"
		// covers the thirtieth, which is what the person holding it was told.
		out.ReasonCode = CoverageOutOfWindow
	case !row.HasServiceLine:
		out.ReasonCode = CoverageServiceNotCovered
	default:
		out.Usable, out.ReasonCode = true, CoverageOK
		out.CoveredQuantity, out.CoveredAmount = row.CoveredQuantity, row.CoveredAmount
		out.CurrencyCode = row.CurrencyCode
	}
	if !out.Usable {
		return out, nil
	}

	usage, err := s.reports.CreateReportUsage(ctx, tx, in.TenantID, NewReportUsageRow{
		ReportID: row.ReportID, UsedByType: in.UsedByType, UsedByID: in.UsedByID,
		UsedAt: s.now().UTC(), ActorID: in.ActorID,
	})
	if err != nil {
		return Coverage{}, err
	}
	id := usage.ID
	out.UsageID = &id
	return out, nil
}

// ErrCoverageReportNotFound is what a coverage call naming a report that is not there
// answers with. It is ErrReportNotFound under another name so a caller can tell "no such
// report" from "that report does not cover this", which are different answers to give a
// claims adjuster.
var ErrCoverageReportNotFound = ErrReportNotFound

// IsCoverageNotFound reports whether an error from ReportCoverage is the report simply not
// being there.
func IsCoverageNotFound(err error) bool { return errors.Is(err, ErrReportNotFound) }

// dayOfValue drops the time of day from a value rather than a pointer.
func dayOfValue(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
