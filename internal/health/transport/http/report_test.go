package healthhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// The permission sets the report lifecycle needs, spelled out rather than referenced so this
// test fails if internal/identity/application/roles.go quietly gains a grant.
const (
	// providerReportPermissions is PROVIDER_STAFF's health half plus the report grant it
	// holds. It writes reports and may never decide about one.
	providerReportPermissions = "health.case.read,health.case.manage,health.clinical.read," +
		"health.medical_report.manage"
	// reviewerReportPermissions is MEDICAL_REVIEWER's: clinical, sensitive and the review
	// grant. It decides and may never write a report.
	reviewerReportPermissions = "health.case.read,health.clinical.read,health.sensitive.read," +
		"health.medical_report.review,worklist.read,worklist.claim"
)

// The clinical strings every scan below hunts for. They are distinctive on purpose: a
// substring scan is only meaningful if nothing else in the document could produce it by
// accident.
const (
	reportType     = "FIZIK_TEDAVI"
	reportSubtype  = "AMBULATUVAR"
	reportSummary  = "Sol dizde artroskopi sonrası altı hafta fizik tedavi gereklidir."
	reportLineNote = "Haftada iki seans, sol diz."
	reviewComment  = "Rapordaki bulgular ile istenen hizmet uyumlu bulundu."
	reportFilename = "psikiyatri-degerlendirme-raporu.pdf"
)

type reportBody struct {
	Id                 uuid.UUID  `json:"id"`
	PersonId           uuid.UUID  `json:"personId"`
	Reference          string     `json:"reference"`
	VersionNo          int        `json:"versionNo"`
	RootReportId       uuid.UUID  `json:"rootReportId"`
	SupersedesReportId *uuid.UUID `json:"supersedesReportId"`
	Status             string     `json:"status"`
	Projection         string     `json:"projection"`
	// The four clinical fields are pointers so "absent" and "empty" are different answers,
	// which is exactly the difference the financial projection turns on.
	ReportType      *string `json:"reportType"`
	ReportSubtype   *string `json:"reportSubtype"`
	ClinicalSummary *string `json:"clinicalSummary"`
	ReviewComment   *string `json:"reviewComment"`
	ValidFrom       string  `json:"validFrom"`
	ValidTo         string  `json:"validTo"`
	RowVersion      int64   `json:"rowVersion"`
	Services        []struct {
		Id                  uuid.UUID `json:"id"`
		ServiceDefinitionId uuid.UUID `json:"serviceDefinitionId"`
		ServiceCode         string    `json:"serviceCode"`
		CoveredQuantity     *string   `json:"coveredQuantity"`
		CoveredAmount       *string   `json:"coveredAmount"`
		CurrencyCode        *string   `json:"currencyCode"`
		Notes               *string   `json:"notes"`
	} `json:"services"`
	Documents []struct {
		Id               uuid.UUID `json:"id"`
		DocumentTypeCode string    `json:"documentTypeCode"`
		OriginalFilename string    `json:"originalFilename"`
	} `json:"documents"`
}

type reportPageBody struct {
	Items []reportBody `json:"items"`
}

type usagePageBody struct {
	Items []struct {
		Id         uuid.UUID `json:"id"`
		ReportId   uuid.UUID `json:"reportId"`
		UsedByType string    `json:"usedByType"`
		UsedById   uuid.UUID `json:"usedById"`
	} `json:"items"`
}

// etagOf reads the ETag a command has to send back as If-Match. Reading it from the header
// rather than from the body is deliberate: If-Match is what a real client would use, and a
// test that took the row version out of the JSON would not notice the header going missing.
func etagOf(t *testing.T, rec interface{ Header() http.Header }) string {
	t.Helper()
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("response carries no ETag")
	}
	return tag
}

// createDraft writes a draft report with a clinical summary, one service line and one linked
// document the scanner has cleared — everything a submission needs.
func (s *server) createDraft(t *testing.T, caseID *uuid.UUID) reportBody {
	t.Helper()
	body := map[string]any{
		"personId":                      s.person,
		"reportType":                    reportType,
		"reportSubtype":                 reportSubtype,
		"issuedAt":                      "2026-06-15",
		"validFrom":                     "2026-06-15",
		"validTo":                       "2026-12-31",
		"clinicalSummary":               reportSummary,
		"issuingProviderOrganizationId": s.provider,
	}
	if caseID != nil {
		body["caseId"] = *caseID
	}
	rec := s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create report = %d: %s", rec.Code, rec.Body.String())
	}
	draft := decode[reportBody](t, rec)
	s.putServices(t, draft.Id, etagOf(t, rec))
	s.linkCleanDocument(t, draft.Id, reportType)
	return s.getReport(t, draft.Id, providerReportPermissions)
}

// putServices replaces the report's lines with one covered service.
func (s *server) putServices(t *testing.T, reportID uuid.UUID, ifMatch string) reportBody {
	t.Helper()
	rec := s.do(t, http.MethodPut, "/api/v1/medical-reports/"+reportID.String()+"/services",
		providerReportPermissions, map[string]any{
			"items": []map[string]any{{
				"serviceDefinitionId": s.definition,
				"coveredQuantity":     "12.000000",
				"coveredAmount":       "9000.000000",
				"currencyCode":        "TRY",
				"notes":               reportLineNote,
			}},
		}, "If-Match", ifMatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("put services = %d: %s", rec.Code, rec.Body.String())
	}
	return decode[reportBody](t, rec)
}

// linkCleanDocument seeds the report file the submit gate looks for: an object the scanner
// cleared, in the secure bucket, linked to the report under a document type the gate accepts.
// It is written straight into the database because what is under test here is the gate, not
// WP-I4-04's upload pipeline.
func (s *server) linkCleanDocument(t *testing.T, reportID uuid.UUID, documentType string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	digest := make([]byte, 32)
	copy(digest, reportID[:])
	var objectID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO document.object (tenant_id, object_key, bucket, classification,
		                             original_filename, content_type, byte_size, sha256,
		                             scan_status, owner_tenant_organization_id)
		VALUES ($1, 'secure/' || $2::text, 'secure', 'HEALTH', $3, 'application/pdf', 1024, $4,
		        'CLEAN', $5)
		RETURNING id`, s.tenant, reportID.String(), reportFilename, digest, s.provider).
		Scan(&objectID); err != nil {
		t.Fatalf("seed document object: %v", err)
	}
	s.h.AdminExec(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id,
		                           document_type_code, required_permission)
		VALUES ($1, $2, 'MEDICAL_REPORT', $3, $4, 'health.clinical.read')`,
		s.tenant, objectID, reportID, documentType)
}

func (s *server) getReport(t *testing.T, id uuid.UUID, permissions string, headers ...string) reportBody {
	t.Helper()
	rec := s.do(t, http.MethodGet, "/api/v1/medical-reports/"+id.String(), permissions, nil, headers...)
	if rec.Code != http.StatusOK {
		t.Fatalf("get report = %d: %s", rec.Code, rec.Body.String())
	}
	return decode[reportBody](t, rec)
}

// command gives one lifecycle command and returns the response, so a test can assert on the
// code as easily as on the body.
//
// The If-Match comes from the stored row rather than from a GET, because a GET is itself a
// clinical read: fetching the version first would put an access event on the log for every
// command and would answer 428 for a report on a sensitive case, neither of which is
// anything to do with what is being tested here.
func (s *server) command(t *testing.T, id uuid.UUID, verb, permissions string, body any) (
	reportBody, string,
) {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/api/v1/medical-reports/"+id.String()+"/"+verb,
		permissions, body, "If-Match", ifMatch(s.rowVersionOf(t, id)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", verb, rec.Code, rec.Body.String())
	}
	return decode[reportBody](t, rec), etagOf(t, rec)
}

// rowVersionOf reads the stored row version, which is what the ETag quotes.
func (s *server) rowVersionOf(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var version int64
	if err := s.h.Admin.QueryRow(ctx,
		`SELECT row_version FROM health.medical_report WHERE id = $1`, id).Scan(&version); err != nil {
		t.Fatalf("read row version: %v", err)
	}
	return version
}

func ifMatch(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// approvedReport takes a draft the whole way to APPROVED.
func (s *server) approvedReport(t *testing.T, caseID *uuid.UUID) reportBody {
	t.Helper()
	draft := s.createDraft(t, caseID)
	s.command(t, draft.Id, "submit", providerReportPermissions, nil)
	s.command(t, draft.Id, "start-review", reviewerReportPermissions, nil)
	approved, _ := s.command(t, draft.Id, "approve", reviewerReportPermissions,
		map[string]any{"reviewComment": reviewComment})
	return approved
}

// reportRow reads the whole stored row as one JSON document. It is what the immutability test
// compares before and after, because comparing fields one by one would pass over a column
// somebody adds later and forgets to check.
func (s *server) reportRow(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var raw []byte
	if err := s.h.Admin.QueryRow(ctx,
		`SELECT to_jsonb(r) FROM health.medical_report r WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read report row: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode report row: %v", err)
	}
	return out
}

func (s *server) chainStatuses(t *testing.T, rootID uuid.UUID) map[string]string {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rows, err := s.h.Admin.Query(ctx, `
		SELECT version_no::text, status FROM health.medical_report
		 WHERE tenant_id = $1 AND root_report_id = $2 ORDER BY version_no`, s.tenant, rootID)
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var version, status string
		if err := rows.Scan(&version, &status); err != nil {
			t.Fatalf("scan chain row: %v", err)
		}
		out[version] = status
	}
	return out
}

// TestApprovedReportIsImmutableAndACorrectionIsANewVersion is the first §3 test and the
// package's whole reason for existing.
//
// Three things are asserted and each of them fails on a different mutation. The two edits
// answer 409, and the stored row is byte-identical after them — remove the freeze check from
// PatchReportDraft or PutReportServices and the comparison names the column that moved.
// Version 2 approves and the chain then holds exactly one APPROVED — remove the supersede
// step from the approval and either the unique index refuses the write or the chain holds
// two. And version 1 keeps every column of its decision — the reviewer, the moment, the
// comment, the summary, the dates and the lines — with only its status moved, which is what
// "the old decision is preserved" means.
func TestApprovedReportIsImmutableAndACorrectionIsANewVersion(t *testing.T) {
	s := newServer(t)
	first := s.approvedReport(t, nil)
	before := s.reportRow(t, first.Id)

	// 1. Neither edit is allowed on a decided report.
	rec := s.do(t, http.MethodPatch, "/api/v1/medical-reports/"+first.Id.String(),
		providerReportPermissions, map[string]any{
			"reportType": "BASKA_TUR", "issuedAt": "2026-06-15",
			"validFrom": "2026-06-15", "validTo": "2027-12-31",
		}, "If-Match", ifMatch(first.RowVersion))
	if rec.Code != http.StatusConflict {
		t.Fatalf("patch on an approved report = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "MEDICAL_REPORT_IMMUTABLE" {
		t.Fatalf("patch refusal code = %s, want MEDICAL_REPORT_IMMUTABLE", code)
	}
	rec = s.do(t, http.MethodPut, "/api/v1/medical-reports/"+first.Id.String()+"/services",
		providerReportPermissions, map[string]any{"items": []map[string]any{}},
		"If-Match", ifMatch(first.RowVersion))
	if rec.Code != http.StatusConflict {
		t.Fatalf("put services on an approved report = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "MEDICAL_REPORT_IMMUTABLE" {
		t.Fatalf("put refusal code = %s, want MEDICAL_REPORT_IMMUTABLE", code)
	}
	// Byte-identical: the two refusals wrote nothing at all.
	assertSameRow(t, before, s.reportRow(t, first.Id), nil)
	lines := s.getReport(t, first.Id, providerReportPermissions).Services
	if len(lines) != 1 {
		t.Fatalf("the refused replacement left %d lines, want the original 1", len(lines))
	}

	// 2. A correction is a new version of the same chain, with the lines copied.
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{"personId": s.person, "supersedesReportId": first.Id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create correction = %d: %s", rec.Code, rec.Body.String())
	}
	second := decode[reportBody](t, rec)
	if second.VersionNo != 2 || second.Reference != first.Reference ||
		second.RootReportId != first.RootReportId {
		t.Fatalf("correction = version %d reference %s root %s, want version 2 of %s/%s",
			second.VersionNo, second.Reference, second.RootReportId, first.Reference, first.RootReportId)
	}
	if second.Status != "DRAFT" {
		t.Fatalf("correction status = %s, want DRAFT: a correction is reviewed like any report", second.Status)
	}
	if len(second.Services) != 1 || second.Services[0].ServiceDefinitionId != s.definition {
		t.Fatalf("correction copied %d lines, want the 1 of version 1", len(second.Services))
	}
	if second.Services[0].Id == first.Services[0].Id {
		t.Fatal("the correction reused version 1's line id; a line belongs to the version that holds it")
	}

	// The correction needs its own document: it is a new report, and the file of the
	// version it corrects is linked to that version.
	s.linkCleanDocument(t, second.Id, reportType)
	s.command(t, second.Id, "submit", providerReportPermissions, nil)
	s.command(t, second.Id, "start-review", reviewerReportPermissions, nil)
	s.command(t, second.Id, "approve", reviewerReportPermissions, nil)

	// 3. Exactly one APPROVED in the chain, and it is the new one.
	statuses := s.chainStatuses(t, first.RootReportId)
	if statuses["1"] != "SUPERSEDED" || statuses["2"] != "APPROVED" {
		t.Fatalf("chain = %v, want version 1 SUPERSEDED and version 2 APPROVED", statuses)
	}
	approved := 0
	for _, status := range statuses {
		if status == "APPROVED" {
			approved++
		}
	}
	if approved != 1 {
		t.Fatalf("the chain has %d approved versions, want exactly 1", approved)
	}

	// 4. Version 1 is exactly as it was decided, but for the status the chain moved. Its
	// lines are untouched and it is still readable.
	assertSameRow(t, before, s.reportRow(t, first.Id),
		map[string]bool{"status": true, "updated_at": true, "updated_by": true, "row_version": true})
	if got := s.reportRow(t, first.Id)["status"]; got != "SUPERSEDED" {
		t.Fatalf("version 1 status = %v, want SUPERSEDED", got)
	}
	old := s.getReport(t, first.Id, providerReportPermissions)
	if len(old.Services) != 1 || old.ReviewComment == nil || *old.ReviewComment != reviewComment {
		t.Fatal("version 1 lost its lines or its reviewer's comment; the old decision must be preserved")
	}
}

// assertSameRow compares two snapshots of one row, ignoring the columns named. A missing
// column is as much a failure as a changed one.
func assertSameRow(t *testing.T, before, after map[string]any, ignore map[string]bool) {
	t.Helper()
	for key, want := range before {
		if ignore[key] {
			continue
		}
		got, ok := after[key]
		if !ok {
			t.Errorf("column %s disappeared from the stored row", key)
			continue
		}
		if !sameJSON(want, got) {
			t.Errorf("column %s changed: %v -> %v", key, want, got)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			t.Errorf("column %s appeared in the stored row", key)
		}
	}
}

func sameJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

// TestSubmitNeedsAServiceLineAndACleanDocument is the second §3 test. Both halves of the gate
// are proved separately, because a single fixture missing both would pass with either check
// removed.
func TestSubmitNeedsAServiceLineAndACleanDocument(t *testing.T) {
	s := newServer(t)

	// A report with a document and no service line.
	noLines := s.createDraft(t, nil)
	rec := s.do(t, http.MethodPut, "/api/v1/medical-reports/"+noLines.Id.String()+"/services",
		providerReportPermissions, map[string]any{"items": []map[string]any{}},
		"If-Match", ifMatch(noLines.RowVersion))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear services = %d: %s", rec.Code, rec.Body.String())
	}
	cleared := decode[reportBody](t, rec)
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports/"+noLines.Id.String()+"/submit",
		providerReportPermissions, nil, "If-Match", ifMatch(cleared.RowVersion))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submit with no line = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "MEDICAL_REPORT_SERVICE_REQUIRED" {
		t.Fatalf("refusal code = %s, want MEDICAL_REPORT_SERVICE_REQUIRED", code)
	}

	// A report with a service line and no cleared document. The link exists — it is the
	// scan verdict that is missing, which is the case the gate is actually for: a file in
	// quarantine is not a document a reviewer can open.
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{
			"personId": s.person, "reportType": reportType, "issuedAt": "2026-06-15",
			"validFrom": "2026-06-15", "validTo": "2026-12-31",
			"issuingProviderOrganizationId": s.provider,
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create report = %d: %s", rec.Code, rec.Body.String())
	}
	noDocument := decode[reportBody](t, rec)
	withLine := s.putServices(t, noDocument.Id, etagOf(t, rec))
	s.linkPendingDocument(t, noDocument.Id)
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports/"+noDocument.Id.String()+"/submit",
		providerReportPermissions, nil, "If-Match", ifMatch(withLine.RowVersion))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submit with no clean document = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "MEDICAL_REPORT_DOCUMENT_REQUIRED" {
		t.Fatalf("refusal code = %s, want MEDICAL_REPORT_DOCUMENT_REQUIRED", code)
	}

	// With both, it submits — and raises a work item whose title carries the reference and
	// no clinical word at all.
	s.linkCleanDocument(t, noDocument.Id, reportType)
	submitted, _ := s.command(t, noDocument.Id, "submit", providerReportPermissions, nil)
	if submitted.Status != "SUBMITTED" {
		t.Fatalf("status after submit = %s, want SUBMITTED", submitted.Status)
	}
	title, queueID := s.workItemOf(t, noDocument.Id)
	if queueID != s.reviewQueue {
		t.Fatalf("work item landed in queue %s, want the medical review queue %s", queueID, s.reviewQueue)
	}
	if !strings.Contains(title, submitted.Reference) {
		t.Fatalf("work item title %q does not carry the report reference %s", title, submitted.Reference)
	}
	for _, clinical := range []string{reportType, reportSubtype, reportSummary, reportLineNote} {
		if strings.Contains(title, clinical) {
			t.Fatalf("work item title %q carries clinical text %q", title, clinical)
		}
	}
}

// linkPendingDocument links a file the scanner has not decided about yet.
func (s *server) linkPendingDocument(t *testing.T, reportID uuid.UUID) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var objectID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO document.object (tenant_id, object_key, bucket, classification,
		                             original_filename, content_type, scan_status,
		                             owner_tenant_organization_id)
		VALUES ($1, 'quarantine/' || $2::text, 'quarantine', 'HEALTH', $3, 'application/pdf',
		        'PENDING', $4)
		RETURNING id`, s.tenant, reportID.String(), reportFilename, s.provider).Scan(&objectID); err != nil {
		t.Fatalf("seed pending document: %v", err)
	}
	s.h.AdminExec(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id,
		                           document_type_code, required_permission)
		VALUES ($1, $2, 'MEDICAL_REPORT', $3, $4, 'health.clinical.read')`,
		s.tenant, objectID, reportID, reportType)
}

func (s *server) workItemOf(t *testing.T, reportID uuid.UUID) (title string, queueID uuid.UUID) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT title, queue_id FROM workflow.work_item
		 WHERE tenant_id = $1 AND aggregate_type = 'MEDICAL_REPORT' AND aggregate_id = $2`,
		s.tenant, reportID).Scan(&title, &queueID); err != nil {
		t.Fatalf("read work item: %v", err)
	}
	return title, queueID
}

// TestReportCoverageIsTheOnlyWayAClaimMayLeanOnAReport is the third §3 test: the port
// WP-I5-04 calls, and the usage row every usable answer leaves behind.
//
// Each of the four questions is asked separately, because a single call could not tell which
// of the three conditions a refusal came from. Ignore the window and the out-of-window case
// becomes usable; drop the service-line join and the wrong-service case does; write the usage
// row for a refusal and the two "no row" assertions fail.
func TestReportCoverageIsTheOnlyWayAClaimMayLeanOnAReport(t *testing.T) {
	s := newServer(t)
	approved := s.approvedReport(t, nil)
	claimID := uuid.New()

	day := func(v string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.DateOnly, v)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	// In the window, on a covered service: usable, with the limits and one usage row.
	answer := s.coverage(t, application.CoverageRequest{
		TenantID: s.tenant, ReportID: approved.Id, ServiceDefinitionID: s.definition,
		ServiceDate: day("2026-08-01"), UsedByType: "CLAIM", UsedByID: claimID,
	})
	if !answer.Usable || answer.ReasonCode != application.CoverageOK {
		t.Fatalf("in-window covered service = usable %t %s, want usable COVERED",
			answer.Usable, answer.ReasonCode)
	}
	if answer.UsageID == nil {
		t.Fatal("a usable answer wrote no usage row; a claim could not prove what it leaned on")
	}
	if answer.VersionNo != approved.VersionNo || answer.Reference != approved.Reference {
		t.Fatalf("answer names version %d of %s, want %d of %s",
			answer.VersionNo, answer.Reference, approved.VersionNo, approved.Reference)
	}
	if answer.CoveredQuantity == nil || *answer.CoveredQuantity != "12.000000" ||
		answer.CoveredAmount == nil || *answer.CoveredAmount != "9000.000000" {
		t.Fatalf("answer carries quantity %v amount %v, want the report's exact decimals",
			answer.CoveredQuantity, answer.CoveredAmount)
	}
	if n := s.usageCount(t, approved.Id); n != 1 {
		t.Fatalf("usage rows after one usable answer = %d, want 1", n)
	}

	// The last day of the window is inside it: a report valid "to the thirty-first" covers
	// the thirty-first, which is what the person holding it was told.
	answer = s.coverage(t, application.CoverageRequest{
		TenantID: s.tenant, ReportID: approved.Id, ServiceDefinitionID: s.definition,
		ServiceDate: day("2026-12-31"), UsedByType: "CLAIM", UsedByID: claimID,
	})
	if !answer.Usable {
		t.Fatalf("the last day of the window = %s, want usable", answer.ReasonCode)
	}

	// Out of the window: not usable, and no row.
	answer = s.coverage(t, application.CoverageRequest{
		TenantID: s.tenant, ReportID: approved.Id, ServiceDefinitionID: s.definition,
		ServiceDate: day("2027-01-01"), UsedByType: "CLAIM", UsedByID: claimID,
	})
	if answer.Usable || answer.ReasonCode != application.CoverageOutOfWindow {
		t.Fatalf("out of window = usable %t %s, want refused REPORT_OUT_OF_WINDOW",
			answer.Usable, answer.ReasonCode)
	}
	if answer.UsageID != nil {
		t.Fatal("a refused answer wrote a usage row; nothing used a report it was not allowed to use")
	}

	// Another service: not usable, and no row.
	answer = s.coverage(t, application.CoverageRequest{
		TenantID: s.tenant, ReportID: approved.Id, ServiceDefinitionID: s.otherDefinition,
		ServiceDate: day("2026-08-01"), UsedByType: "CLAIM", UsedByID: claimID,
	})
	if answer.Usable || answer.ReasonCode != application.CoverageServiceNotCovered {
		t.Fatalf("uncovered service = usable %t %s, want refused SERVICE_NOT_IN_REPORT",
			answer.Usable, answer.ReasonCode)
	}
	if n := s.usageCount(t, approved.Id); n != 2 {
		t.Fatalf("usage rows after two usable and two refused answers = %d, want 2", n)
	}

	// The trace is readable, and it names this version.
	rec := s.do(t, http.MethodGet, "/api/v1/medical-reports/"+approved.Id.String()+"/usages",
		providerReportPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list usages = %d: %s", rec.Code, rec.Body.String())
	}
	usages := decode[usagePageBody](t, rec)
	if len(usages.Items) != 2 {
		t.Fatalf("usage list has %d rows, want 2", len(usages.Items))
	}
	for _, row := range usages.Items {
		if row.ReportId != approved.Id || row.UsedByType != "CLAIM" || row.UsedById != claimID {
			t.Fatalf("usage row %+v does not name the claim that used this version", row)
		}
	}
}

// coverage calls the port the way WP-I5-04 will: inside a transaction the caller owns.
func (s *server) coverage(t *testing.T, in application.CoverageRequest) application.Coverage {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var out application.Coverage
	err := db.WithTenantTx(ctx, s.h.App, db.TenantContext{TenantID: s.tenant},
		func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = s.svc.ReportCoverage(ctx, tx, in)
			return err
		})
	if err != nil {
		t.Fatalf("report coverage: %v", err)
	}
	return out
}

func (s *server) usageCount(t *testing.T, reportID uuid.UUID) int {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var n int
	if err := s.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM health.medical_report_usage WHERE tenant_id = $1 AND report_id = $2`,
		s.tenant, reportID).Scan(&n); err != nil {
		t.Fatalf("count usages: %v", err)
	}
	return n
}

// TestSponsorHRCannotSeeAReportsClinicalHalf is the fourth §3 test: the WP-I5-01 scan, over a
// treatment report.
//
// It is written against the bytes rather than the decoded struct for the same reason the case
// version is: a test asserting `body.ClinicalSummary == nil` would pass if the summary came
// back under some other key or nested inside something the struct ignores. Remove
// projectReport from the service and this fails on the scan, naming the string it found.
func TestSponsorHRCannotSeeAReportsClinicalHalf(t *testing.T) {
	s := newServer(t)
	approved := s.approvedReport(t, nil)
	// Building the report involved clinical reads of its own, so the assertion below is
	// that the sponsor's reads add nothing rather than that the log is empty.
	eventsBefore := len(s.accessEvents(t, approved.Id))

	// 1. The single read, as the sponsor's HR user.
	rec := s.do(t, http.MethodGet, "/api/v1/medical-reports/"+approved.Id.String(),
		sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sponsor HR get report = %d: %s", rec.Code, rec.Body.String())
	}
	financialBytes := rec.Body.String()
	financial := decode[reportBody](t, rec)
	if financial.Projection != "FINANCIAL" {
		t.Fatalf("sponsor HR was served the %s projection", financial.Projection)
	}
	if financial.ReportType != nil || financial.ReportSubtype != nil ||
		financial.ClinicalSummary != nil || financial.ReviewComment != nil {
		t.Fatalf("financial projection carries clinical fields: %+v", financial)
	}
	if len(financial.Documents) != 0 {
		t.Fatalf("financial projection carries %d documents; a document list is a diagnosis on a filename",
			len(financial.Documents))
	}
	// What it does carry is what a financial reviewer needs to reconcile a claim against.
	if financial.Reference != approved.Reference || financial.Status != "APPROVED" ||
		financial.ValidFrom == "" || financial.ValidTo == "" {
		t.Fatalf("financial projection lost the reference, status or dates: %+v", financial)
	}
	if len(financial.Services) != 1 {
		t.Fatalf("financial projection carries %d service lines, want the covered services", len(financial.Services))
	}
	line := financial.Services[0]
	if line.CoveredQuantity == nil || *line.CoveredQuantity != "12.000000" ||
		line.CoveredAmount == nil || *line.CoveredAmount != "9000.000000" {
		t.Fatalf("financial projection lost the covered limits: %+v", line)
	}
	if line.Notes != nil {
		t.Fatalf("financial projection carries the line note %q", *line.Notes)
	}

	// 2. The list is the same answer.
	rec = s.do(t, http.MethodGet, "/api/v1/medical-reports?personId="+s.person.String(),
		sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sponsor HR list reports = %d: %s", rec.Code, rec.Body.String())
	}
	listBytes := rec.Body.String()
	page := decode[reportPageBody](t, rec)
	if len(page.Items) != 1 || page.Items[0].Projection != "FINANCIAL" {
		t.Fatalf("sponsor HR list = %d rows in projection %v", len(page.Items), page.Items)
	}

	// 3. The scan. Neither body may contain any of the clinical strings, anywhere.
	for _, document := range []struct{ what, body string }{
		{"the single read", financialBytes}, {"the list", listBytes},
	} {
		for _, clinical := range []string{reportType, reportSubtype, reportSummary,
			reportLineNote, reviewComment, reportFilename} {
			if strings.Contains(document.body, clinical) {
				t.Fatalf("%s of the financial projection contains %q:\n%s",
					document.what, clinical, document.body)
			}
		}
	}

	// 4. Neither read wrote an access event: the financial projection carries nothing
	// clinical, so there is no clinical access to record.
	if got := len(s.accessEvents(t, approved.Id)); got != eventsBefore {
		t.Fatalf("the financial projection wrote %d access events, want none", got-eventsBefore)
	}

	// 5. The clinical reader sees everything, and the look is on the record.
	clinical := s.getReport(t, approved.Id, providerReportPermissions)
	if clinical.Projection != "CLINICAL" {
		t.Fatalf("the clinician was served the %s projection", clinical.Projection)
	}
	if clinical.ReportType == nil || *clinical.ReportType != reportType ||
		clinical.ClinicalSummary == nil || *clinical.ClinicalSummary != reportSummary ||
		clinical.ReviewComment == nil || *clinical.ReviewComment != reviewComment {
		t.Fatalf("the clinical projection is missing something: %+v", clinical)
	}
	if len(clinical.Documents) != 1 || clinical.Documents[0].OriginalFilename != reportFilename {
		t.Fatalf("the clinical projection carries %d documents, want the report file", len(clinical.Documents))
	}
	if clinical.Services[0].Notes == nil || *clinical.Services[0].Notes != reportLineNote {
		t.Fatal("the clinical projection lost the line note")
	}
	events := s.accessEvents(t, approved.Id)
	if len(events) == 0 {
		t.Fatal("a clinical read of a report wrote no access event")
	}
	last := events[len(events)-1]
	if last.Classification != "HEALTH" || last.Outcome != "SUCCESS" || last.AccessType != "VIEW" ||
		last.PersonID == nil || *last.PersonID != s.person {
		t.Fatalf("the access event of a clinical report read = %+v", last)
	}
	if resource := s.accessResourceType(t, approved.Id); resource != "MEDICAL_REPORT" {
		t.Fatalf("access event resource type = %s, want MEDICAL_REPORT", resource)
	}
}

func (s *server) accessResourceType(t *testing.T, resourceID uuid.UUID) string {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var out string
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT resource_type FROM audit.access_event
		 WHERE tenant_id = $1 AND resource_id = $2 ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		s.tenant, resourceID).Scan(&out); err != nil {
		t.Fatalf("read access event resource type: %v", err)
	}
	return out
}

// TestReportOfASensitiveCaseFollowsItsCase proves the report reuses WP-I5-01's decision
// rather than carrying a rule of its own: a report on a sensitive case is refused to a
// clinical reader without the sensitive grant, demands a purpose from one that holds it, and
// records both.
func TestReportOfASensitiveCaseFollowsItsCase(t *testing.T) {
	s := newServer(t)
	caseID, _ := s.openCase(t, s.codeStrict, "PRIMARY")
	report := s.approvedReport(t, &caseID)

	// A clinical reader without the sensitive grant gets the financial projection, never a
	// refusal: a refusal would itself say the case carries a protected category.
	rec := s.do(t, http.MethodGet, "/api/v1/medical-reports/"+report.Id.String(),
		providerReportPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("clinician read of a sensitive report = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decode[reportBody](t, rec); got.Projection != "FINANCIAL" || got.ClinicalSummary != nil {
		t.Fatalf("a sensitive report was served to a caller with no sensitive grant: %+v", got)
	}

	// The sensitive grant without a purpose is 428, and the refusal is recorded.
	rec = s.do(t, http.MethodGet, "/api/v1/medical-reports/"+report.Id.String(),
		reviewerReportPermissions, nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("sensitive report with no purpose = %d, want 428: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "ACCESS_PURPOSE_REQUIRED" {
		t.Fatalf("refusal code = %s, want ACCESS_PURPOSE_REQUIRED", code)
	}

	// With both, the clinical projection — and the purpose on the access event.
	full := s.getReport(t, report.Id, reviewerReportPermissions,
		"X-Access-Purpose", "MEDICAL_REVIEW", "X-Access-Reason", "Rapor%20incelemesi")
	if full.Projection != "CLINICAL" || full.ClinicalSummary == nil {
		t.Fatalf("the reviewer with a stated purpose was served %+v", full)
	}
	var denied, success bool
	for _, event := range s.accessEvents(t, report.Id) {
		switch event.Outcome {
		case "DENIED":
			denied = true
		case "SUCCESS":
			if event.Purpose != nil && *event.Purpose == "MEDICAL_REVIEW" &&
				event.Reason != nil && *event.Reason == "Rapor incelemesi" {
				success = true
			}
		}
	}
	if !denied || !success {
		t.Fatalf("access events of a sensitive report read: denied=%t success-with-purpose=%t",
			denied, success)
	}
}

// TestDecidingAReportNotifiesWithoutSayingAnythingClinical proves this package is the
// publisher WP-I5-05 left the template for, and that what it publishes could be read out
// loud in a waiting room.
func TestDecidingAReportNotifiesWithoutSayingAnythingClinical(t *testing.T) {
	s := newServer(t)
	approved := s.approvedReport(t, nil)

	payloads := s.notificationPayloads(t)
	if len(payloads) == 0 {
		t.Fatal("approving a report published no medical_report.decided notification")
	}
	for _, payload := range payloads {
		if !strings.Contains(payload, approved.Reference) {
			t.Fatalf("the notification does not carry the report reference: %s", payload)
		}
		if !strings.Contains(payload, "APPROVED") {
			t.Fatalf("the notification does not carry the status word: %s", payload)
		}
		for _, clinical := range []string{reportType, reportSubtype, reportSummary,
			reportLineNote, reviewComment} {
			if strings.Contains(payload, clinical) {
				t.Fatalf("the notification carries clinical text %q: %s", clinical, payload)
			}
		}
	}
	// Both sides are told: the member, and the provider that issued the report.
	recipients := s.notificationRecipients(t)
	if !recipients[s.person] || !recipients[s.provider] {
		t.Fatalf("notification recipients = %v, want the member and the issuing provider", recipients)
	}
}

func (s *server) notificationPayloads(t *testing.T) []string {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rows, err := s.h.Admin.Query(ctx, `
		SELECT payload_json::text FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = 'notification.message.requested'
		   AND payload_json->>'eventCode' = 'medical_report.decided'`, s.tenant)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

func (s *server) notificationRecipients(t *testing.T) map[uuid.UUID]bool {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rows, err := s.h.Admin.Query(ctx, `
		SELECT aggregate_id FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = 'notification.message.requested'
		   AND payload_json->>'eventCode' = 'medical_report.decided'`, s.tenant)
	if err != nil {
		t.Fatalf("read outbox recipients: %v", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan recipient: %v", err)
		}
		out[id] = true
	}
	return out
}

// TestTheExpiryJobIsIdempotent is the fifth §3 test.
//
// The property is not "the second run reports zero" — another tenant's report could be
// expiring at the same moment — but "the second run does not touch a row the first one
// finished". The row version is what proves that: an UPDATE that matched would move it,
// whether or not the status ended up the same.
func TestTheExpiryJobIsIdempotent(t *testing.T) {
	s := newServer(t)
	expired := s.approvedReportValidUntil(t, "2026-01-31")
	current := s.approvedReport(t, nil)

	ctx, cancel := s.h.Ctx()
	defer cancel()
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	first, err := s.svc.ExpireReports(ctx, now)
	if err != nil {
		t.Fatalf("first expiry sweep: %v", err)
	}
	if first != 1 {
		t.Fatalf("the first sweep expired %d reports, want exactly the one past its validity", first)
	}
	if status := s.reportRow(t, expired.Id)["status"]; status != "EXPIRED" {
		t.Fatalf("a report past its validity is %v, want EXPIRED", status)
	}
	if status := s.reportRow(t, current.Id)["status"]; status != "APPROVED" {
		t.Fatalf("a report inside its validity is %v, want APPROVED", status)
	}
	after := s.reportRow(t, expired.Id)

	second, err := s.svc.ExpireReports(ctx, now)
	if err != nil {
		t.Fatalf("second expiry sweep: %v", err)
	}
	// The row is untouched, column for column. A sweep that listed or updated an already
	// expired report would move updated_at and row_version even if the status did not change.
	assertSameRow(t, after, s.reportRow(t, expired.Id), nil)
	if second != 0 {
		t.Fatalf("the second sweep expired %d reports, want none left to expire", second)
	}
}

// approvedReportValidUntil takes a report the whole way to APPROVED with a validity that has
// already run out. The dates are in the past on both ends: an approval does not check the
// window, because a reviewer approving a report for treatment that already happened is an
// ordinary thing to do.
func (s *server) approvedReportValidUntil(t *testing.T, validTo string) reportBody {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{
			"personId": s.person, "reportType": reportType, "issuedAt": "2026-01-01",
			"validFrom": "2026-01-01", "validTo": validTo,
			"issuingProviderOrganizationId": s.provider,
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create report = %d: %s", rec.Code, rec.Body.String())
	}
	draft := decode[reportBody](t, rec)
	s.putServices(t, draft.Id, etagOf(t, rec))
	s.linkCleanDocument(t, draft.Id, reportType)
	s.command(t, draft.Id, "submit", providerReportPermissions, nil)
	s.command(t, draft.Id, "start-review", reviewerReportPermissions, nil)
	approved, _ := s.command(t, draft.Id, "approve", reviewerReportPermissions, nil)
	return approved
}

// TestOnlyADecidedReportMayBeCorrected proves the other half of the version rule: a draft is
// corrected by editing it, and only the newest version of a chain may be superseded — a fork
// would make "which version is in force" a question with two answers.
func TestOnlyADecidedReportMayBeCorrected(t *testing.T) {
	s := newServer(t)
	draft := s.createDraft(t, nil)

	rec := s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{"personId": s.person, "supersedesReportId": draft.Id})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("correcting a draft = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "MEDICAL_REPORT_SUPERSEDES_INVALID" {
		t.Fatalf("refusal code = %s, want MEDICAL_REPORT_SUPERSEDES_INVALID", code)
	}

	first := s.approvedReport(t, nil)
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{"personId": s.person, "supersedesReportId": first.Id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first correction = %d: %s", rec.Code, rec.Body.String())
	}
	// A second correction of the same version would fork the chain.
	rec = s.do(t, http.MethodPost, "/api/v1/medical-reports", providerReportPermissions,
		map[string]any{"personId": s.person, "supersedesReportId": first.Id})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second correction of one version = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// TestClaimingTheWorkItemStartsTheReview proves the hook WP-I4-03 gained: a reviewer who
// works from the queue never has to say separately that they have started.
func TestClaimingTheWorkItemStartsTheReview(t *testing.T) {
	s := newServer(t)
	draft := s.createDraft(t, nil)
	submitted, _ := s.command(t, draft.Id, "submit", providerReportPermissions, nil)
	if submitted.Status != "SUBMITTED" {
		t.Fatalf("status after submit = %s", submitted.Status)
	}

	ctx, cancel := s.h.Ctx()
	defer cancel()
	var itemID uuid.UUID
	var version int64
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT id, row_version FROM workflow.work_item
		 WHERE tenant_id = $1 AND aggregate_type = 'MEDICAL_REPORT' AND aggregate_id = $2`,
		s.tenant, draft.Id).Scan(&itemID, &version); err != nil {
		t.Fatalf("read work item: %v", err)
	}

	if _, err := s.workflows.ClaimItem(context.Background(),
		reviewerContext(s), itemID, version); err != nil {
		t.Fatalf("claim work item: %v", err)
	}
	if status := s.reportRow(t, draft.Id)["status"]; status != "UNDER_REVIEW" {
		t.Fatalf("status after the work item was claimed = %v, want UNDER_REVIEW", status)
	}
}

// reviewerContext is the request context a medical reviewer would arrive with. The worklist
// service is called directly rather than over HTTP because this package mounts no worklist
// routes: what is under test is the seam between the two modules, not WP-I4-03's transport.
func reviewerContext(s *server) identity.RequestContext {
	rc := identity.RequestContext{
		TenantID: s.tenant, MembershipID: s.membership,
		Principal: identity.Principal{ActorID: s.actor}, StepUpValid: true,
		Permissions: map[string]struct{}{},
	}
	for _, permission := range strings.Split(reviewerReportPermissions, ",") {
		rc.Permissions[permission] = struct{}{}
	}
	return rc
}
