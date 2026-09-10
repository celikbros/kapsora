package application_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// render drives the worker's own handler over the export the API queued, exactly as the outbox
// dispatcher would.
func (f *fixture) render(t *testing.T, export application.Export) {
	t.Helper()
	ctx, cancel := f.ctx()
	defer cancel()
	payload, err := json.Marshal(map[string]any{"exportId": export.ID})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if err := f.reports.HandleExportRequested(ctx, outbox.Delivery{
		TenantID:      uuid.NullUUID{UUID: f.tenant, Valid: true},
		AggregateType: domain.AggregateExport, AggregateID: export.ID,
		Type: application.ExportRequestedEvent, Payload: payload,
	}); err != nil {
		t.Fatalf("render the export: %v", err)
	}
}

// bytesOf reads the rendered file back out of the object store.
func (f *fixture) bytesOf(t *testing.T, export application.Export) [][]string {
	t.Helper()
	if export.DocumentID == nil {
		t.Fatal("the export has no document")
	}
	key := f.scalar(t, `SELECT object_key FROM document.object WHERE tenant_id = $1 AND id = $2`,
		f.tenant, *export.DocumentID)
	body, ok := f.store.Bytes(secureBucket, key)
	if !ok {
		t.Fatalf("no bytes stored under %s", key)
	}
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\ufeff"))))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read the rendered file: %v", err)
	}
	return records
}

// seedSettlementsWorthExporting writes three settlements a SETTLEMENTS export will carry.
func (f *fixture) seedSettlementsWorthExporting(t *testing.T) {
	t.Helper()
	for i, amount := range []string{"1000", "2000.50", "300"} {
		batch := f.seedBatch(t, f.provider, runDay, amount, amount, "0")
		f.seedSettlement(t, f.provider, batch, runDay.AddDate(0, 0, i), amount, "0", "APPROVED")
	}
}

// **The file carries the watermark on every row.**
//
// The whole path, end to end: the API queues, the worker renders, the bytes land in the store, and
// the test reads them back. Nothing here trusts the renderer — the assertion is about the bytes.
func TestARenderedExportCarriesTheWatermarkOnEveryRow(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedSettlementsWorthExporting(t)

	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
		Parameters: map[string]any{"status": "APPROVED"},
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	if export.Status != domain.ExportQueued {
		t.Fatalf("a new export is %s, want QUEUED", export.Status)
	}
	if export.Watermark == "" {
		t.Fatal("the export carries no watermark")
	}
	// The event the worker waits for, published inside the command's own transaction.
	if n := f.count(t, `SELECT count(*) FROM system.outbox_event
	                     WHERE tenant_id = $1 AND event_type = $2 AND aggregate_id = $3`,
		f.tenant, application.ExportRequestedEvent, export.ID); n != 1 {
		t.Errorf("the export published %d events, want exactly one", n)
	}

	f.render(t, export)

	ready, err := f.reports.GetExport(ctx, f.readerRC(), export.ID, false)
	if err != nil {
		t.Fatalf("read the export back: %v", err)
	}
	if ready.Status != domain.ExportReady || ready.DocumentID == nil {
		t.Fatalf("the export is %s with document %v, want READY with a file",
			ready.Status, ready.DocumentID)
	}
	if ready.RowCount != 3 {
		t.Errorf("the export says %d rows, want the three settlements", ready.RowCount)
	}

	records := f.bytesOf(t, ready)
	if len(records) != 5 {
		t.Fatalf("the file holds %d lines, want the stamp, the header and three rows",
			len(records))
	}
	if records[0][0] != ready.Watermark {
		t.Errorf("the sheet header is %q, want the watermark", records[0][0])
	}
	for i, row := range records[1:] {
		if row[0] != domain.WatermarkColumn && row[0] != ready.Watermark {
			t.Errorf("line %d begins with %q, want the watermark", i+1, row[0])
		}
	}
	for i, row := range records[2:] {
		if row[0] != ready.Watermark {
			t.Errorf("data row %d is unstamped: %q", i, row[0])
		}
	}
	// The watermark names the person, the tenant and this export.
	if !strings.Contains(ready.Watermark, ready.ID.String()) {
		t.Errorf("the watermark %q does not name the export", ready.Watermark)
	}
	if !strings.Contains(ready.Watermark, "Mali Değerlendirici") {
		t.Errorf("the watermark %q does not name the requester", ready.Watermark)
	}

	// The file is a WP-I4-04 document like any other: scanned clean, in the secure bucket, and
	// linked back to the export it came from.
	if got := f.scalar(t, `SELECT scan_status || '/' || bucket FROM document.object
	                        WHERE tenant_id = $1 AND id = $2`, f.tenant, *ready.DocumentID); got != "CLEAN/secure" {
		t.Errorf("the export file is %s, want CLEAN in the secure bucket", got)
	}
	if n := f.count(t, `SELECT count(*) FROM document.link
	                     WHERE tenant_id = $1 AND object_id = $2 AND aggregate_id = $3
	                       AND required_permission = 'report.export'`,
		f.tenant, *ready.DocumentID, ready.ID); n != 1 {
		t.Errorf("the file is linked to its export %d times under the export permission, want once", n)
	}
}

// **CLAIMS without the sensitive grant is refused**, and with it the rows carry the line
// descriptions that are the reason for the grant.
func TestClaimsExportNeedsTheSensitiveGrant(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedClaim(t, f.provider, "APPROVED", "500", "Kalp kateterizasyonu", fixtureNow.AddDate(0, 0, -3))

	_, err := f.reports.CreateExport(ctx, f.plainExporterRC(), application.NewExportInput{
		Kind: domain.KindClaims, Format: domain.FormatCSV,
	})
	if !errors.Is(err, application.ErrExportSensitive) {
		t.Fatalf("a caller without report.export.sensitive got %v, want the refusal", err)
	}
	if n := f.count(t, `SELECT count(*) FROM report.export WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Errorf("the refusal still wrote %d export rows", n)
	}

	// The same request, with the grant.
	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindClaims, Format: domain.FormatCSV,
	})
	if err != nil {
		t.Fatalf("create claims export: %v", err)
	}
	f.render(t, export)
	ready, err := f.reports.GetExport(ctx, f.readerRC(), export.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	records := f.bytesOf(t, ready)
	found := false
	for _, row := range records[2:] {
		if slicesContain(row, "Kalp kateterizasyonu") {
			found = true
		}
		if row[0] != ready.Watermark {
			t.Errorf("a claims row is unstamped: %q", row[0])
		}
	}
	if !found {
		t.Error("the claims export carries no line description, which is what the grant is for")
	}
}

// **Every download is an access event, and the count moves with it.**
func TestEveryDownloadIsCountedAndAudited(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedSettlementsWorthExporting(t)

	export := f.readyExport(t, domain.KindSettlements)

	for want := 1; want <= 2; want++ {
		url, downloaded, err := f.reports.DownloadExport(ctx, f.readerRC(), export.ID,
			"BILLING_DISPUTE", "sağlayıcı itirazı", false)
		if err != nil {
			t.Fatalf("download %d: %v", want, err)
		}
		if url.URL == "" || url.Method != "GET" {
			t.Errorf("download %d handed back %+v, want a presigned GET", want, url)
		}
		if downloaded.DownloadCount != want {
			t.Errorf("download %d says the count is %d", want, downloaded.DownloadCount)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM audit.access_event
	                     WHERE tenant_id = $1 AND resource_type = 'EXPORT' AND resource_id = $2
	                       AND access_type = 'EXPORT' AND outcome = 'SUCCESS'`,
		f.tenant, export.ID); n != 2 {
		t.Errorf("two downloads wrote %d export access events, want one each", n)
	}
	// The document store writes its own, naming the file rather than the export. Two rows for one
	// act, and each answers a question the other cannot.
	if n := f.count(t, `SELECT count(*) FROM audit.access_event
	                     WHERE tenant_id = $1 AND resource_type = 'DOCUMENT'
	                       AND access_type = 'DOWNLOAD'`, f.tenant); n != 2 {
		t.Errorf("the document store wrote %d download events, want one per download", n)
	}
	// The reason the export was opened travels into the row, with the kind in front of it.
	reason := f.scalar(t, `SELECT reason_text FROM audit.access_event
	                        WHERE tenant_id = $1 AND resource_id = $2 LIMIT 1`, f.tenant, export.ID)
	if !strings.Contains(reason, domain.KindSettlements) || !strings.Contains(reason, "itiraz") {
		t.Errorf("the access event's reason is %q, want the kind and what the person wrote", reason)
	}
}

// **After the TTL the download is refused**, and the refusal is itself an audited access.
func TestADownloadAfterTheTTLIsRefusedAndAudited(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedSettlementsWorthExporting(t)

	export := f.readyExport(t, domain.KindSettlements)
	// The clock is pinned, so the TTL is moved rather than waited for. It is the same column the
	// service compares against, written the way the nightly sweep would find it.
	f.h.AdminExec(`UPDATE report.export
	                  SET requested_at = requested_at - interval '48 hours',
	                      expires_at   = requested_at - interval '24 hours'
	                WHERE tenant_id = $1 AND id = $2`, f.tenant, export.ID)

	_, _, err := f.reports.DownloadExport(ctx, f.readerRC(), export.ID, "", "", false)
	if !errors.Is(err, application.ErrExportExpired) {
		t.Fatalf("an expired export was downloadable: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM report.export
	                     WHERE tenant_id = $1 AND id = $2 AND download_count = 0`,
		f.tenant, export.ID); n != 1 {
		t.Error("a refused download was counted")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.access_event
	                     WHERE tenant_id = $1 AND resource_id = $2 AND outcome = 'DENIED'`,
		f.tenant, export.ID); n != 1 {
		t.Errorf("the refusal wrote %d denied access events, want exactly one", n)
	}
}

// **The nightly sweep removes the file and marks the row EXPIRED.** The row outlives the bytes:
// "this export existed, carried this many rows and stopped being downloadable on this day" is an
// answer somebody will need.
func TestTheNightlySweepRemovesTheFileAndMarksTheRow(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedSettlementsWorthExporting(t)

	export := f.readyExport(t, domain.KindSettlements)
	key := f.scalar(t, `SELECT object_key FROM document.object WHERE tenant_id = $1 AND id = $2`,
		f.tenant, *export.DocumentID)
	if _, ok := f.store.Bytes(secureBucket, key); !ok {
		t.Fatal("the file was never stored")
	}

	// Nothing to do while the export is inside its TTL.
	quiet, err := f.reports.ExpireExports(ctx, fixtureNow)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if quiet.Expired != 0 {
		t.Errorf("the sweep expired %d exports that are still inside their TTL", quiet.Expired)
	}

	report, err := f.reports.ExpireExports(ctx, export.ExpiresAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if report.Expired != 1 || report.Held != 0 {
		t.Fatalf("the sweep expired %d and held %d, want 1 and 0", report.Expired, report.Held)
	}
	if _, ok := f.store.Bytes(secureBucket, key); ok {
		t.Error("the bytes of an expired export are still in the store")
	}
	status := f.scalar(t, `SELECT status FROM report.export WHERE tenant_id = $1 AND id = $2`,
		f.tenant, export.ID)
	if status != domain.ExportExpired {
		t.Errorf("the expired export is %s, want EXPIRED", status)
	}
	if n := f.count(t, `SELECT count(*) FROM report.export
	                     WHERE tenant_id = $1 AND id = $2 AND row_count = 3`,
		f.tenant, export.ID); n != 1 {
		t.Error("the expired row lost the count of what it carried")
	}
	// A second pass finds nothing left to do.
	again, err := f.reports.ExpireExports(ctx, export.ExpiresAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again.Expired != 0 {
		t.Errorf("the second sweep expired %d exports again", again.Expired)
	}
	// And the row now refuses the download on its own status rather than on the clock.
	if _, _, err := f.reports.DownloadExport(ctx, f.readerRC(), export.ID, "", "", false); !errors.Is(
		err, application.ErrExportExpired) {
		t.Errorf("an expired export answered %v", err)
	}
}

// **The parameters column holds no identifier** — the database says so, whatever the service does.
func TestTheParametersColumnRefusesAnIdentifier(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	if _, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
		Parameters: map[string]any{"providerId": f.provider.String()},
	}); err == nil {
		t.Fatal("an export carrying a provider id in its filters was accepted")
	}

	// The scope, which is where an identifier belongs, is a column of its own and is accepted.
	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
		ProviderOrganizationID: &f.provider,
		Parameters:             map[string]any{"status": "APPROVED"},
	})
	if err != nil {
		t.Fatalf("a scoped export was refused: %v", err)
	}
	stored := f.scalar(t, `SELECT parameters::text FROM report.export
	                        WHERE tenant_id = $1 AND id = $2`, f.tenant, export.ID)
	if strings.Contains(stored, f.provider.String()) {
		t.Errorf("the stored filters carry the provider id: %s", stored)
	}
}

// An export is scoped to the provider it names, and the file carries nobody else's rows.
func TestAScopedExportCarriesOnlyThatProvidersRows(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	mine := f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")
	f.seedSettlement(t, f.provider, mine, runDay, "1000", "0", "APPROVED")
	theirs := f.seedBatch(t, f.rival, runDay, "9999", "9999", "0")
	f.seedSettlement(t, f.rival, theirs, runDay, "9999", "0", "APPROVED")

	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
		ProviderOrganizationID: &f.provider,
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	f.render(t, export)
	ready, err := f.reports.GetExport(ctx, f.readerRC(), export.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if ready.RowCount != 1 {
		t.Fatalf("the scoped export carries %d rows, want only its own provider's", ready.RowCount)
	}
	for _, row := range f.bytesOf(t, ready)[2:] {
		if slicesContain(row, "9999") {
			t.Errorf("the export leaked another provider's figure: %v", row)
		}
	}
}

// A provider-scoped caller may not export somebody else's provider.
func TestAProviderMayNotExportAnotherProvider(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	_, err := f.reports.CreateExport(ctx, f.providerRC(f.rival), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
		ProviderOrganizationID: &f.provider,
	})
	if !errors.Is(err, application.ErrProviderScope) {
		t.Fatalf("a rival queued an export of somebody else's provider: %v", err)
	}
}

// A redelivered render produces one file, not two. The QUEUED-to-RUNNING move carries the status
// in its WHERE clause, so the second delivery finds nothing to do.
func TestARedeliveredRenderProducesOneFile(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedSettlementsWorthExporting(t)

	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	f.render(t, export)
	f.render(t, export)

	if n := f.count(t, `SELECT count(*) FROM document.object WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Errorf("two deliveries produced %d files, want one", n)
	}
	ready, err := f.reports.GetExport(ctx, f.readerRC(), export.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if ready.Status != domain.ExportReady {
		t.Errorf("the export is %s after a redelivery, want READY", ready.Status)
	}
}

// A queued export cannot be downloaded, and the caller is told which kind of "no" it is.
func TestAQueuedExportIsNotDownloadable(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	if _, _, err := f.reports.DownloadExport(ctx, f.readerRC(), export.ID, "", "", false); !errors.Is(
		err, application.ErrExportNotReady) {
		t.Fatalf("a queued export answered %v, want EXPORT_NOT_READY", err)
	}
}

// The TTL is the tenant's, and a tenant that has configured one gets it rather than the default.
func TestTheExpiryFollowsTheTenantsSetting(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	f.h.AdminExec(`INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
	               VALUES ($1, 'report.export_ttl_hours', '4'::jsonb)`, f.tenant)
	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: domain.KindSettlements, Format: domain.FormatCSV,
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	if got := export.ExpiresAt.Sub(export.RequestedAt); got != 4*time.Hour {
		t.Errorf("the export lives %s, want the tenant's four hours", got)
	}
}

// readyExport queues one export of a kind and renders it, returning the finished row.
func (f *fixture) readyExport(t *testing.T, kind string) application.Export {
	t.Helper()
	ctx, cancel := f.ctx()
	defer cancel()
	export, err := f.reports.CreateExport(ctx, f.readerRC(), application.NewExportInput{
		Kind: kind, Format: domain.FormatCSV,
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	f.render(t, export)
	ready, err := f.reports.GetExport(ctx, f.readerRC(), export.ID, false)
	if err != nil {
		t.Fatalf("read the export back: %v", err)
	}
	if ready.Status != domain.ExportReady {
		t.Fatalf("the export is %s, want READY", ready.Status)
	}
	return ready
}

func slicesContain(row []string, want string) bool {
	for _, cell := range row {
		if cell == want {
			return true
		}
	}
	return false
}
