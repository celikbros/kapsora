package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/report/domain"
	"github.com/celikbros/kapsora/internal/report/settings"
)

// ExportClassification is the document class every export file is stored under. It is
// CONFIDENTIAL rather than INTERNAL because an export is a list of what a payer owes and what it
// paid — commercially sensitive whichever kind it is — and it is not PERSONAL or HEALTH because
// no export in this package carries a person's identity or a diagnosis. The CLAIMS kind carries
// line descriptions, which is why it needs a grant of its own rather than a different class:
// classification decides how a file is handled, and the permission decides who may make one.
const ExportClassification = "CONFIDENTIAL"

// CreateExport queues one export and asks the worker for it. Nothing is rendered here: an API
// request that produced a file would be a request whose duration is a function of how much data
// the tenant has.
//
// Three things are decided in this transaction and nowhere else: the watermark, the expiry and
// whether the caller may ask for this kind at all. The watermark is stored, not rebuilt, so the
// string on the screen and the string in the file are the same string. The expiry is a stored
// moment, not a duration read at download time, so the answer to "may I still open this" does not
// change because somebody edited a setting this afternoon.
func (s *Service) CreateExport(ctx context.Context, rc identity.RequestContext,
	in NewExportInput,
) (Export, error) {
	if err := domain.ValidateExportRequest(in.Kind, in.Format, in.ProviderOrganizationID != nil,
		in.PeriodFrom, in.PeriodTo, in.CurrencyCode, in.Parameters); err != nil {
		return Export{}, err
	}
	// **The sensitive kind needs the sensitive grant.** It is checked here rather than in the
	// transport because it is a property of what is being asked for, not of the route: the same
	// endpoint produces four harmless kinds and one that carries prose about people.
	if domain.SensitiveKind(in.Kind) && !rc.Has(PermissionExportSensitive) {
		return Export{}, ErrExportSensitive
	}
	if in.ProviderOrganizationID != nil && !scopeOf(rc).Allows(*in.ProviderOrganizationID) {
		return Export{}, ErrProviderScope
	}
	parameters := in.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return Export{}, fieldError("parameters", "FORMAT", "filtreler okunamadı")
	}

	requestedAt := s.now().UTC()
	var out Export
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		values, err := settings.Load(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		tenantCode, requesterName, err := s.repo.ExportIdentity(ctx, tx, rc.TenantID,
			rc.Principal.ActorID)
		if err != nil {
			return err
		}
		// The id is drawn here so that the watermark can carry it: a stamp that named the export
		// only after the row existed would need a second write, and a file whose stamp and whose
		// row could be written apart is a file whose stamp can be missing.
		exportID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		watermark := domain.Watermark(tenantCode, requesterName, requestedAt, exportID.String())

		created, err := s.repo.CreateExport(ctx, tx, rc.TenantID, NewExportRow{
			ID: exportID, Kind: in.Kind, Format: in.Format, Parameters: encoded,
			ProviderOrganizationID: in.ProviderOrganizationID,
			PeriodFrom:             in.PeriodFrom, PeriodTo: in.PeriodTo,
			CurrencyCode: optionalPtr(in.CurrencyCode),
			RequestedBy:  rc.Principal.ActorID, RequestedAt: requestedAt,
			ExpiresAt: requestedAt.Add(values.ExportTTL), Watermark: watermark,
		})
		if err != nil {
			return err
		}
		if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
			TenantID: nullUUID(rc.TenantID), AggregateType: domain.AggregateExport,
			AggregateID: created.ID, Type: ExportRequestedEvent,
			Payload:          map[string]any{"exportId": created.ID},
			DeduplicationKey: ExportRequestedEvent + ":" + created.ID.String(),
		}); err != nil {
			return err
		}
		out = created
		return s.record(ctx, tx, rc, "report.export.create", domain.AggregateExport, created.ID,
			map[string]any{
				"kind": created.Kind, "format": created.Format,
				"sensitive":       domain.SensitiveKind(created.Kind),
				"ttl_hours":       int(values.ExportTTL.Hours()),
				"parameter_count": len(parameters),
			})
	})
	if err != nil {
		return Export{}, err
	}
	return out, nil
}

// GetExport reads one export. `own` narrows the read to the caller's own rows, which is what the
// transport asks for unless the caller holds the tenant-wide grant.
func (s *Service) GetExport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	own bool,
) (Export, error) {
	var out Export
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetExport(ctx, tx, rc.TenantID, id, requesterFilter(rc, own))
		return err
	})
	if err != nil {
		return Export{}, err
	}
	return out, nil
}

// ListExports pages the tenant's exports, newest first.
func (s *Service) ListExports(ctx context.Context, rc identity.RequestContext, f ExportFilter,
) (ExportPage, error) {
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ExportPage{}, err
	}
	if f.Kind != "" && !domain.ValidKind(f.Kind) {
		return ExportPage{}, fieldError("kind", "ENUM", "geçerli bir dışa aktarma türü olmalı")
	}
	if f.Status != "" && !domain.ValidExportStatus(f.Status) {
		return ExportPage{}, fieldError("status", "ENUM", "geçerli bir dışa aktarma durumu olmalı")
	}

	var page ExportPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListExports(ctx, tx, rc.TenantID, ExportQueryOptions{
			RequestedBy: requesterFilter(rc, f.Mine), Kind: f.Kind, Status: f.Status,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			page.NextCursor = s.cursors.Encode(exportCursor(rows[len(rows)-1]))
		}
		page.Items = rows
		return nil
	})
	if err != nil {
		return ExportPage{}, err
	}
	return page, nil
}

// requesterFilter narrows a read to the caller's own exports when it should be narrowed.
func requesterFilter(rc identity.RequestContext, own bool) *uuid.UUID {
	if !own {
		return nil
	}
	return actorPtr(rc.Principal.ActorID)
}

// DownloadExport hands back a short-lived link to the file, and only for an export that is ready
// and has not expired.
//
// **The download is the access that is audited.** Two rows are written, and each answers a
// question the other cannot: this module writes an `audit.access_event` naming the export, its
// kind and the requester, and WP-I4-04's own download writes one naming the document. A privacy
// review asking "who took the claims export out in March" reads the first; a document review
// asking "who opened this file" reads the second.
//
// The count and the access event are written before the link is handed out, in the same
// transaction: a link that was issued and not counted is a download nobody knows happened, and
// that is the exact failure this whole package exists to prevent.
func (s *Service) DownloadExport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	purposeCode, reasonText string, own bool,
) (objectstore.PresignedURL, Export, error) {
	var (
		export Export
		ready  Export
	)
	now := s.now().UTC()
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		found, err := s.repo.GetExport(ctx, tx, rc.TenantID, id, requesterFilter(rc, own))
		if err != nil {
			return err
		}
		export = found
		if err := downloadableOrError(found, now); err != nil {
			return err
		}
		count, err := s.repo.CountExportDownload(ctx, tx, rc.TenantID, found.ID)
		if err != nil {
			return err
		}
		found.DownloadCount = count
		ready = found
		return s.recordExportAccess(ctx, tx, rc, found, purposeCode, reasonText,
			audit.OutcomeSuccess)
	})
	if errors.Is(err, ErrExportExpired) {
		// A refused download of an expired export is exactly the row a review looks for, so it
		// is written in a transaction of its own: the refusal rolls the first one back, and an
		// audit row that rolls back with the thing it was auditing is an audit row nobody ever
		// sees.
		if auditErr := s.recordExportDenial(ctx, rc, export, purposeCode, reasonText); auditErr != nil {
			return objectstore.PresignedURL{}, Export{}, auditErr
		}
		return objectstore.PresignedURL{}, Export{}, err
	}
	if err != nil {
		return objectstore.PresignedURL{}, Export{}, err
	}

	// The document's own download: it checks the scan status, refuses a purged file and writes
	// the document access event. It runs in a transaction of its own, which is the ordinary cost
	// of asking another module for something.
	url, err := s.documents.Download(ctx, rc, *ready.DocumentID, purposeCode, reasonText)
	if err != nil {
		return objectstore.PresignedURL{}, Export{}, err
	}
	return url, ready, nil
}

// downloadableOrError turns "not downloadable" into the one reason it is not. The four answers
// are deliberately different: a queued export is a wait, a failed one is not, an expired one is
// gone for good, and one whose document has been purged is a file retention already removed.
func downloadableOrError(e Export, now time.Time) error {
	switch {
	case e.Status == domain.ExportFailed:
		return ErrExportFailed
	case e.Status == domain.ExportExpired || e.Expired(now):
		// The order matters: an EXPIRED row and a READY row past its moment are the same answer
		// to the caller, and the sweep that writes the first one runs at night.
		return ErrExportExpired
	case e.Status != domain.ExportReady || e.DocumentID == nil:
		return ErrExportNotReady
	default:
		return nil
	}
}

// recordExportAccess writes the access event a download produces, whichever way it went.
func (s *Service) recordExportAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	e Export, purposeCode, reasonText string, outcome audit.Outcome,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		MembershipID: nullUUID(rc.MembershipID),
		ResourceType: domain.AggregateExport, ResourceID: nullUUID(e.ID),
		AccessType: audit.AccessExport, Classification: audit.ClassConfidential,
		PurposeCode: purposeCode,
		// The kind travels in the reason so the row says what left the building without a join.
		// It is prefixed rather than replacing what the caller wrote, because both matter.
		ReasonText: exportReason(e.Kind, reasonText), Outcome: outcome,
	})
}

func (s *Service) recordExportDenial(ctx context.Context, rc identity.RequestContext, e Export,
	purposeCode, reasonText string,
) error {
	if e.ID == uuid.Nil {
		return nil
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		return s.recordExportAccess(ctx, tx, rc, e, purposeCode, reasonText, audit.OutcomeDenied)
	})
}

// exportReason renders the reason text of an access event. `audit.access_event.reason_text` is
// prose a person wrote; the kind is prefixed to it so the row is readable on its own.
func exportReason(kind, reasonText string) string {
	if reasonText == "" {
		return kind
	}
	return kind + ": " + reasonText
}

// HandleExportRequested renders one queued export: it reads the rows, writes the file with the
// watermark on every one of them, stores it through WP-I4-04 and marks the export READY.
//
// It is the worker's, and it is idempotent by predicate rather than by flag. `MarkExportRunning`
// carries the status in its WHERE clause, so a redelivered event finds an export that is already
// running or already finished and stops; and a render that fails leaves FAILED with a code rather
// than an export that is QUEUED for ever.
func (s *Service) HandleExportRequested(ctx context.Context, ev outbox.Delivery) error {
	var payload struct {
		ExportID uuid.UUID `json:"exportId"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("report: decode export event: %w", err)
	}
	if !ev.TenantID.Valid || payload.ExportID == uuid.Nil {
		return errors.New("report: the export event names no tenant or no export")
	}
	tenantID := ev.TenantID.UUID

	var (
		export Export
		table  ExportTable
		claim  bool
	)
	if err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		found, err := s.repo.LockExport(ctx, tx, tenantID, payload.ExportID)
		if err != nil {
			return err
		}
		export = found
		if found.Status != domain.ExportQueued {
			return nil
		}
		started, err := s.repo.MarkExportRunning(ctx, tx, tenantID, found.ID)
		if err != nil {
			return err
		}
		claim = started
		return nil
	}); err != nil {
		return err
	}
	if !claim {
		// Somebody else has it, or it is already finished. Either way there is nothing to do,
		// and a second file would be a second watermark for one request.
		return nil
	}

	if err := s.renderExport(ctx, tenantID, export, &table); err != nil {
		s.logger.Error("report: export render failed", "tenant_id", tenantID,
			"export_id", export.ID, "kind", export.Kind, "error", err)
		return s.failExport(ctx, tenantID, export, failureCodeFor(err))
	}
	return nil
}

// renderExport does the work: rows, file, document, READY.
func (s *Service) renderExport(ctx context.Context, tenantID uuid.UUID, export Export,
	table *ExportTable,
) error {
	if err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ExportRows(ctx, tx, tenantID, ExportRowQuery{
			Kind: export.Kind, ProviderOrganizationID: export.ProviderOrganizationID,
			PeriodFrom: export.PeriodFrom, PeriodTo: export.PeriodTo,
			CurrencyCode: export.CurrencyCode,
			// The worker acts for the system: it renders what the export was scoped to when it
			// was created, and the caller's own boundary was applied then. A scope read here
			// would be nobody's.
			Scope: Scope{}, RowLimit: domain.MaxExportRows + 1,
		})
		if err != nil {
			return err
		}
		*table = rows
		return nil
	}); err != nil {
		return err
	}
	if len(table.Rows) > domain.MaxExportRows {
		return ErrTooManyRows
	}

	// **The watermark goes on every row.** It is applied here, once, by the renderer, so a
	// column somebody adds to one of the queries later cannot arrive unstamped.
	body := domain.RenderCSV(export.Watermark, table.Columns, table.Rows)
	documentID, err := s.documents.Store(ctx, tenantID, RenderedFile{
		// `text/csv` without a charset parameter: WP-I4-04's media type pattern is a bare
		// type/subtype, and the encoding travels in the file itself as a byte order mark, which
		// is what the program that opens it actually reads.
		Filename: exportFilename(export), ContentType: "text/csv",
		Classification: ExportClassification, Body: body,
		OwnerOrganizationID: export.ProviderOrganizationID,
		RetainFor:           export.ExpiresAt.Sub(export.RequestedAt),
		ExportID:            export.ID, Kind: export.Kind,
	})
	if err != nil {
		return err
	}

	rowCount := len(table.Rows)
	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.MarkExportReady(ctx, tx, tenantID, export.ID, documentID,
			rowCount); err != nil {
			return err
		}
		return s.recordSystem(ctx, tx, tenantID, "report.export.ready", domain.AggregateExport,
			export.ID, map[string]any{
				"kind": export.Kind, "format": export.Format, "row_count": rowCount,
				"byte_size": len(body),
			})
	})
}

// failExport records why a render could not be completed. It is a separate transaction from the
// render for the reason every failure record is: the transaction that failed is the one that
// rolled back.
func (s *Service) failExport(ctx context.Context, tenantID uuid.UUID, export Export,
	failureCode string,
) error {
	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.MarkExportFailed(ctx, tx, tenantID, export.ID, failureCode); err != nil {
			return err
		}
		return s.recordSystem(ctx, tx, tenantID, "report.export.failed", domain.AggregateExport,
			export.ID, map[string]any{"kind": export.Kind, "failure_code": failureCode})
	})
}

// failureCodeFor maps a render failure to the code stored on the row. `ck_report_export_failure_code`
// bounds what may be written, and a code a screen can switch on is worth more than a message a
// screen has to show verbatim.
func failureCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrTooManyRows):
		return "TOO_MANY_ROWS"
	case errors.Is(err, ErrDocumentUnavailable):
		return "DOCUMENT_STORE_UNAVAILABLE"
	default:
		return "RENDER_FAILED"
	}
}

// exportFilename is what the file is called when it lands on somebody's disk. It carries the kind
// and the day and never a provider name: a filename is copied into e-mails, and a payer's list of
// what it owes one named provider is not a thing to write on the outside of the envelope.
func exportFilename(e Export) string {
	return fmt.Sprintf("kapsora-%s-%s.csv", lowerKind(e.Kind),
		e.RequestedAt.UTC().Format("20060102-150405"))
}

func lowerKind(kind string) string {
	out := make([]rune, 0, len(kind))
	for _, r := range kind {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		case r == '_':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
