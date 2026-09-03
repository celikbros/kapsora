package memberimport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Outbox event types of the pipeline. Both handlers are idempotent, so a redelivery
// re-reads the batch and finishes whatever the previous attempt left undone.
const (
	// StagedEvent asks the worker to validate and match a batch too large to process
	// inside the upload request.
	StagedEvent = "party.member_import.staged"
	// ApplyEvent asks the worker to write an accepted batch to the live tables.
	ApplyEvent = "party.member_import.apply"
)

// Aggregate type of both events.
const aggregateType = "party.import_batch"

// Processing limits (WP-I2-05 section 2.3).
const (
	// InlineRowLimit is the largest file validated inside the upload request; beyond it
	// the request answers 202 and the worker takes over.
	InlineRowLimit = 5_000
	// ChunkSize is the number of rows one staging, validation or apply transaction
	// covers. Apply is transactional per chunk and resumable between chunks.
	ChunkSize = 500
)

// Service implements the member import use cases.
type Service struct {
	pool    *pgxpool.Pool
	cipher  crypto.FieldCipher
	index   crypto.BlindIndexer
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	logger  *slog.Logger

	inlineRowLimit int
	chunkSize      int
	now            func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Cipher  crypto.FieldCipher
	Index   crypto.BlindIndexer
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// InlineRowLimit and ChunkSize override the defaults; tests use them to exercise
	// the queued path and the chunk boundary without building huge files.
	InlineRowLimit int
	ChunkSize      int
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Cipher == nil || d.Index == nil || d.Cursors == nil {
		return nil, errors.New("memberimport: pool, cipher, blind indexer and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.InlineRowLimit <= 0 {
		d.InlineRowLimit = InlineRowLimit
	}
	if d.ChunkSize <= 0 {
		d.ChunkSize = ChunkSize
	}
	return &Service{
		pool: d.Pool, cipher: d.Cipher, index: d.Index, audit: d.Audit, cursors: d.Cursors,
		logger: d.Logger, inlineRowLimit: d.InlineRowLimit, chunkSize: d.ChunkSize,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Get returns one batch of the caller's tenant with its counters.
func (s *Service) Get(ctx context.Context, rc identity.RequestContext, batchID uuid.UUID) (Batch, error) {
	var out Batch
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		row, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		out = batchView(row)
		counters, err := readCounters(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		out.Counters = counters
		return nil
	})
	return out, err
}

// ListFilter is the API-level list request.
type ListFilter struct {
	Status string
	Cursor string
	Limit  int
}

// List returns one keyset page of batches, newest first.
func (s *Service) List(ctx context.Context, rc identity.RequestContext, f ListFilter) (BatchPage, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(BatchStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if err := ve.OrNil(); err != nil {
		return BatchPage{}, err
	}
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return BatchPage{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	params := sqlcgen.ListImportBatchesParams{TenantID: rc.TenantID, PageSize: int32(pageSize + 1)} //nolint:gosec // clamped to MaxPageSize
	if f.Status != "" {
		params.Status = &f.Status
	}
	if hasCursor {
		at := cursor.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: cursor.ID, Valid: true}
	}

	var rows []sqlcgen.ListImportBatchesRow
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = sqlcgen.New(tx).ListImportBatches(ctx, params)
		return err
	})
	if err != nil {
		return BatchPage{}, fmt.Errorf("memberimport: list batches: %w", err)
	}
	page := BatchPage{Items: make([]Batch, 0, len(rows))}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		page.Items = append(page.Items, batchView(sqlcgen.GetImportBatchRow(r)))
	}
	return page, nil
}

// RowFilter is the API-level review-queue request.
type RowFilter struct {
	Status string
	Cursor string
	Limit  int
}

// ListRows returns one page of staged rows in file order.
func (s *Service) ListRows(ctx context.Context, rc identity.RequestContext, batchID uuid.UUID, f RowFilter) (RowPage, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(RowStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if err := ve.OrNil(); err != nil {
		return RowPage{}, err
	}
	after, err := decodeRowCursor(f.Cursor)
	if err != nil {
		return RowPage{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	params := sqlcgen.ListImportRowsParams{
		TenantID: rc.TenantID, BatchID: batchID, AfterRowNo: after,
		PageSize: int32(pageSize + 1), //nolint:gosec // clamped to MaxPageSize
	}
	if f.Status != "" {
		params.Status = &f.Status
	}

	var rows []sqlcgen.ListImportRowsRow
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := loadBatch(ctx, tx, rc.TenantID, batchID); err != nil {
			return err
		}
		var err error
		rows, err = sqlcgen.New(tx).ListImportRows(ctx, params)
		return err
	})
	if err != nil {
		return RowPage{}, err
	}
	page := RowPage{Items: make([]Row, 0, len(rows))}
	if len(rows) > pageSize {
		page.NextCursor = encodeRowCursor(rows[pageSize-1].RowNo)
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		view, err := rowView(stagedRow(r))
		if err != nil {
			return RowPage{}, err
		}
		page.Items = append(page.Items, view)
	}
	return page, nil
}

// ReviewInput is the operator's decision for one row.
type ReviewInput struct {
	Decision        string
	MatchedPersonID uuid.UUID
	ExpectedVersion int64
}

// Review records the decision for a CONFLICT, INVALID, VALID or MATCHED row. An INVALID
// row accepts SKIP only: forcing a row with unusable columns into the registry is not a
// review decision, it is a corrupt import.
func (s *Service) Review(ctx context.Context, rc identity.RequestContext, batchID, rowID uuid.UUID, in ReviewInput) (Row, error) {
	ve := &domain.ValidationError{}
	if !domain.Contains(Decisions, in.Decision) {
		ve.Add("decision", "ENUM", "CREATE, UPDATE veya SKIP olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Row{}, err
	}

	var out Row
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		batch, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		if batch.Status != StatusReview && batch.Status != StatusReady && batch.Status != StatusValidating {
			return ErrStateInvalid
		}
		row, err := q.GetImportRow(ctx, sqlcgen.GetImportRowParams{TenantID: rc.TenantID, BatchID: batchID, ID: rowID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRowNotFound
		}
		if err != nil {
			return fmt.Errorf("memberimport: get row: %w", err)
		}
		if row.Status == RowApplied {
			return ErrRowNotReviewable
		}
		if row.Status == RowInvalid && in.Decision != DecisionSkip {
			return ErrRowNotReviewable
		}

		params := sqlcgen.ReviewImportRowParams{
			Decision: strPtr(in.Decision), TenantID: rc.TenantID, BatchID: batchID,
			ID: rowID, RowVersion: in.ExpectedVersion,
			DecidedBy: nullUUID(rc.Principal.ActorID),
		}
		switch in.Decision {
		case DecisionSkip:
			params.Status = RowSkipped
		case DecisionCreate:
			params.Status = RowValid
		case DecisionUpdate:
			if in.MatchedPersonID == uuid.Nil {
				ve.Add("matchedPersonId", "REQUIRED", "güncellenecek kişi zorunlu")
				return ve
			}
			if _, err := q.GetPersonForImport(ctx, sqlcgen.GetPersonForImportParams{
				TenantID: rc.TenantID, ID: in.MatchedPersonID,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					ve.Add("matchedPersonId", "UNKNOWN", "kişi bulunamadı")
					return ve
				}
				return fmt.Errorf("memberimport: review person: %w", err)
			}
			params.Status = RowMatched
			params.MatchedPersonID = nullUUID(in.MatchedPersonID)
		}
		affected, err := q.ReviewImportRow(ctx, params)
		if err != nil {
			return fmt.Errorf("memberimport: review row: %w", err)
		}
		if affected == 0 {
			return ErrVersionMismatch
		}
		if err := s.refreshBatchState(ctx, tx, rc.TenantID, batchID, batch.Status); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "member_import.row.review", rowID, map[string]any{
			"import_batch_id": batchID, "decision": in.Decision, "row_no": int(row.RowNo),
		}); err != nil {
			return err
		}
		updated, err := q.GetImportRow(ctx, sqlcgen.GetImportRowParams{TenantID: rc.TenantID, BatchID: batchID, ID: rowID})
		if err != nil {
			return fmt.Errorf("memberimport: reload row: %w", err)
		}
		out, err = rowView(stagedRow(sqlcgen.ListImportRowsRow(updated)))
		return err
	})
	return out, err
}

// Apply moves an accepted batch to APPLYING and queues the worker job. Applying a batch
// that is already APPLIED is a no-op that returns the batch unchanged, so a repeated
// command (or a repeated delivery of the event) never writes twice.
func (s *Service) Apply(ctx context.Context, rc identity.RequestContext, batchID uuid.UUID, expectedVersion int64) (Batch, error) {
	var out Batch
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		batch, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		if batch.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if batch.Status == StatusApplied || batch.Status == StatusApplying {
			out = batchView(batch)
			out.Counters, err = readCounters(ctx, tx, rc.TenantID, batchID)
			return err
		}
		if batch.Status != StatusReview && batch.Status != StatusReady {
			return ErrStateInvalid
		}
		affected, err := sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
			Status: StatusApplying, ErrorSummary: batch.ErrorSummary, TenantID: rc.TenantID,
			ID: batchID, FromStatuses: []string{StatusReview, StatusReady},
		})
		if err != nil {
			return fmt.Errorf("memberimport: start apply: %w", err)
		}
		if affected == 0 {
			return ErrStateInvalid
		}
		if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
			TenantID: nullUUID(rc.TenantID), AggregateType: aggregateType, AggregateID: batchID,
			Type:             ApplyEvent,
			Payload:          map[string]any{"importBatchId": batchID},
			DeduplicationKey: ApplyEvent + ":" + batchID.String(),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "member_import.apply", batchID, map[string]any{
			"row_count": int(batch.RowCount), "source_system": batch.SourceSystem,
		}); err != nil {
			return err
		}
		reloaded, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		out = batchView(reloaded)
		out.Counters, err = readCounters(ctx, tx, rc.TenantID, batchID)
		return err
	})
	return out, err
}

// Cancel stops a batch that has not started applying.
func (s *Service) Cancel(ctx context.Context, rc identity.RequestContext, batchID uuid.UUID, expectedVersion int64, reason string) (Batch, error) {
	var out Batch
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		batch, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		if batch.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if batch.Status == StatusApplying || batch.Status == StatusApplied || batch.Status == StatusCancelled {
			return ErrStateInvalid
		}
		summary := "CANCELLED"
		if reason != "" {
			summary = "CANCELLED: " + truncate(reason, 480)
		}
		affected, err := sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
			Status: StatusCancelled, ErrorSummary: &summary, TenantID: rc.TenantID, ID: batchID,
			FromStatuses: []string{StatusReceived, StatusValidating, StatusReview, StatusReady, StatusFailed},
		})
		if err != nil {
			return fmt.Errorf("memberimport: cancel: %w", err)
		}
		if affected == 0 {
			return ErrStateInvalid
		}
		if err := s.record(ctx, tx, rc, "member_import.cancel", batchID, map[string]any{
			"row_count": int(batch.RowCount),
		}); err != nil {
			return err
		}
		reloaded, err := loadBatch(ctx, tx, rc.TenantID, batchID)
		if err != nil {
			return err
		}
		out = batchView(reloaded)
		out.Counters, err = readCounters(ctx, tx, rc.TenantID, batchID)
		return err
	})
	return out, err
}

// refreshBatchState recomputes the counters and moves a waiting batch between REVIEW and
// READY as rows are decided.
func (s *Service) refreshBatchState(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID, current string) error {
	counters, err := readCounters(ctx, tx, tenantID, batchID)
	if err != nil {
		return err
	}
	if err := writeCounters(ctx, tx, tenantID, batchID, counters); err != nil {
		return err
	}
	next := StatusReady
	if counters.Review > 0 {
		next = StatusReview
	}
	if current != StatusReview && current != StatusReady && current != StatusValidating {
		return nil
	}
	if next == current {
		return nil
	}
	_, err = sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
		Status: next, TenantID: tenantID, ID: batchID,
		FromStatuses: []string{StatusValidating, StatusReview, StatusReady},
	})
	if err != nil {
		return fmt.Errorf("memberimport: refresh batch status: %w", err)
	}
	return nil
}

// record writes one business audit row; detail carries ids, codes and counts only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, action string, resourceID uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action,
		ResourceType: "member_import", ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// loadBatch reads one batch; RLS hides other tenants, so a foreign id is not found.
func loadBatch(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) (sqlcgen.GetImportBatchRow, error) {
	row, err := sqlcgen.New(tx).GetImportBatch(ctx, sqlcgen.GetImportBatchParams{TenantID: tenantID, ID: batchID})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetImportBatchRow{}, ErrNotFound
	}
	if err != nil {
		return sqlcgen.GetImportBatchRow{}, fmt.Errorf("memberimport: get batch: %w", err)
	}
	return row, nil
}

func readCounters(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) (Counters, error) {
	row, err := sqlcgen.New(tx).ImportBatchCounters(ctx, sqlcgen.ImportBatchCountersParams{TenantID: tenantID, BatchID: batchID})
	if err != nil {
		return Counters{}, fmt.Errorf("memberimport: counters: %w", err)
	}
	return Counters{
		Valid: int(row.ValidCount), Invalid: int(row.InvalidCount), Matched: int(row.MatchedCount),
		Conflict: int(row.ConflictCount), Created: int(row.CreatedCount), Updated: int(row.UpdatedCount),
		Skipped: int(row.SkippedCount), Review: int(row.ReviewCount), PendingApply: int(row.PendingApplyCount),
	}, nil
}

func writeCounters(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID, c Counters) error {
	_, err := sqlcgen.New(tx).SetImportBatchCounters(ctx, sqlcgen.SetImportBatchCountersParams{
		ValidCount: int32(c.Valid), InvalidCount: int32(c.Invalid), MatchedCount: int32(c.Matched), //nolint:gosec // bounded by MaxRows
		ConflictCount: int32(c.Conflict), CreatedCount: int32(c.Created), //nolint:gosec // bounded by MaxRows
		UpdatedCount: int32(c.Updated), SkippedCount: int32(c.Skipped), //nolint:gosec // bounded by MaxRows
		TenantID: tenantID, ID: batchID,
	})
	if err != nil {
		return fmt.Errorf("memberimport: write counters: %w", err)
	}
	return nil
}

func batchView(r sqlcgen.GetImportBatchRow) Batch {
	b := Batch{
		ID: r.ID, SponsorOrganizationID: r.SponsorTenantOrganizationID,
		SourceSystem: r.SourceSystem, SourceVersion: r.SourceVersion, FileName: r.FileName,
		FileSHA256: hex.EncodeToString(r.FileSha256), Format: r.Format, RowCount: int(r.RowCount),
		Status: r.Status, ErrorSummary: r.ErrorSummary, CreatedAt: r.CreatedAt,
		AppliedAt: r.AppliedAt, RowVersion: r.RowVersion,
		Counters: Counters{
			Valid: int(r.ValidCount), Invalid: int(r.InvalidCount), Matched: int(r.MatchedCount),
			Conflict: int(r.ConflictCount), Created: int(r.CreatedCount), Updated: int(r.UpdatedCount),
			Skipped: int(r.SkippedCount),
		},
	}
	if r.PlanID.Valid {
		id := r.PlanID.UUID
		b.PlanID = &id
	}
	return b
}

// stagedRow is the shape shared by the three staging row reads.
type stagedRow sqlcgen.ListImportRowsRow

func rowView(r stagedRow) (Row, error) {
	payload, err := decodePayload(r.Payload)
	if err != nil {
		return Row{}, fmt.Errorf("memberimport: decode payload: %w", err)
	}
	ids, err := decodeIdentifiers(r.Identifiers)
	if err != nil {
		return Row{}, fmt.Errorf("memberimport: decode identifiers: %w", err)
	}
	rowErrors, err := decodeErrors(r.Errors)
	if err != nil {
		return Row{}, fmt.Errorf("memberimport: decode errors: %w", err)
	}
	out := Row{
		ID: r.ID, RowNo: int(r.RowNo), SourceRecordID: r.SourceRecordID, Status: r.Status,
		DisplayName: domain.DisplayName(payload.FirstName, payload.MiddleName, payload.LastName),
		Decision:    r.Decision, Errors: rowErrors, RowVersion: r.RowVersion,
		Identifiers: make([]MaskedIdentifier, 0, len(ids)),
	}
	for _, id := range ids {
		out.Identifiers = append(out.Identifiers, MaskedIdentifier{Type: id.Type, MaskedValue: id.Masked})
	}
	if d, ok := parseDate(payload.BirthDate); ok {
		out.BirthDate = &d
	}
	if payload.Role != "" {
		role := payload.Role
		out.MembershipType = &role
	}
	if payload.PrincipalRecordID != "" {
		ref := payload.PrincipalRecordID
		out.PrincipalSourceRecordID = &ref
	}
	if payload.PlanCode != "" {
		code := payload.PlanCode
		out.PlanCode = &code
	}
	if r.MatchedPersonID.Valid {
		id := r.MatchedPersonID.UUID
		out.MatchedPersonID = &id
	}
	if r.AppliedPersonID.Valid {
		id := r.AppliedPersonID.UUID
		out.AppliedPersonID = &id
	}
	for _, raw := range payload.CandidatePersonIDs {
		if id, err := uuid.Parse(raw); err == nil {
			out.CandidatePersonIDs = append(out.CandidatePersonIDs, id)
		}
	}
	return out, nil
}

// Row cursors are the last row_no of the page: rows are numbered densely from one and
// never move, so a signed keyset cursor would only add weight.
func encodeRowCursor(rowNo int32) string { return fmt.Sprintf("r%d", rowNo) }

func decodeRowCursor(raw string) (int32, error) {
	if raw == "" {
		return 0, nil
	}
	var n int32
	if _, err := fmt.Sscanf(raw, "r%d", &n); err != nil || n < 0 {
		return 0, httpx.ErrInvalidCursor
	}
	return n, nil
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

func dateValue(s string) pgtype.Date {
	d, ok := parseDate(s)
	if !ok {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: d, Valid: true}
}

func dateString(d pgtype.Date) string {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return ""
	}
	return d.Time.Format(time.DateOnly)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// crypto purposes used by the pipeline; the staging envelope shares the person
// identifier key so the apply step can re-encrypt without a second key.
const identifierPurpose = crypto.PurposePersonIdentifier
