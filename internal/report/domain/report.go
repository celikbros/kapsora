// Package domain holds what a report may ask for and what an export may be: the closed lists
// the schema repeats as CHECK constraints, the one string that is stamped on every row of every
// file this platform hands out, and the validation of everything a caller may send. It depends
// on nothing outside the standard library, so every rule here is unit-testable without a
// database, an object store or a clock.
//
// Two rules are stated here as functions rather than as habits somewhere upstream.
//
// Watermark builds the line that goes on the file. There is exactly one way to build it, so a
// file found on somebody's laptop can be read back to the export row that produced it — and so
// the string on the screen and the string in the file cannot drift apart.
//
// ParametersAreAnonymous decides whether a filter blob may be stored. It is the Go half of
// `report.parameters_are_anonymous`, and the two are deliberately the same two tests: an export's
// filters may say what was asked for and may never say who it was about. The database has the
// last word — a service that forgot to call this is refused by the CHECK — and this exists so
// the caller gets a field error rather than a 500.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// FieldError names one invalid request field; Field uses the JSON path of the contract.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError aggregates field errors for a 422 response.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("report: %d validation error(s)", len(e.Fields))
}

// Add appends one field error.
func (e *ValidationError) Add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// Len reports how many field errors were collected.
func (e *ValidationError) Len() int { return len(e.Fields) }

// OrNil returns nil when nothing failed, so callers can `return ve.OrNil()`.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("report: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// The aggregate types this module's audit rows, access events and work items are recorded
// under.
const (
	AggregateExport               = "EXPORT"
	AggregateReconciliationRun    = "RECONCILIATION_RUN"
	QueueReconciliationDifference = "RECONCILIATION_DIFFERENCE"
)

// Export kinds (report.export.kind).
const (
	KindProviderStatement = "PROVIDER_STATEMENT"
	KindBatch             = "BATCH"
	KindSettlements       = "SETTLEMENTS"
	KindClaims            = "CLAIMS"
	KindReconciliation    = "RECONCILIATION"
)

// Export formats. XLSX is in the schema's word list and is refused here, because nothing in
// this repository can write one: there is no spreadsheet dependency, and an export that
// answered READY with a CSV inside a file called .xlsx would be worse than a refusal. The
// contract says the same thing in the same words.
const (
	FormatCSV  = "CSV"
	FormatXLSX = "XLSX"
)

// Export statuses (report.export.status).
const (
	ExportQueued  = "QUEUED"
	ExportRunning = "RUNNING"
	ExportReady   = "READY"
	ExportFailed  = "FAILED"
	ExportExpired = "EXPIRED"
)

// Reconciliation scopes (billing.reconciliation_run.scope).
const (
	ScopeTenantWide = "TENANT"
	ScopeProvider   = "PROVIDER"
)

// Reconciliation run statuses.
const (
	RunBalanced    = "BALANCED"
	RunDifferences = "DIFFERENCES"
	RunFailed      = "FAILED"
)

// The kinds of disagreement a run records. ERP_MISMATCH is M9's and is never written today;
// it is named here so the word list a screen switches on does not change when it is.
const (
	DifferenceUnderpaid       = "UNDERPAID"
	DifferencePaidSumMismatch = "PAID_SUM_MISMATCH"
	DifferenceERPMismatch     = "ERP_MISMATCH"
)

// The aging buckets of the dashboard's claim figure, in the order a screen draws them. They are
// the strings `DashboardClaimAging` produces, and they are listed here so a caller that sees no
// claims in a bucket can still draw the empty column.
const (
	BucketDayZeroToOne     = "D0_1"
	BucketDayTwoToSeven    = "D2_7"
	BucketDayEightToThirty = "D8_30"
	BucketDayThirtyOnePlus = "D31_PLUS"
)

// Closed lists the database repeats as CHECK constraints.
var (
	Kinds          = []string{KindProviderStatement, KindBatch, KindSettlements, KindClaims, KindReconciliation}
	Formats        = []string{FormatCSV, FormatXLSX}
	ExportStatuses = []string{ExportQueued, ExportRunning, ExportReady, ExportFailed, ExportExpired}
	Scopes         = []string{ScopeTenantWide, ScopeProvider}
	RunStatuses    = []string{RunBalanced, RunDifferences, RunFailed}
	AgingBuckets   = []string{BucketDayZeroToOne, BucketDayTwoToSeven, BucketDayEightToThirty, BucketDayThirtyOnePlus}
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxWatermark mirrors ck_report_export_watermark.
	MaxWatermark = 500
	// MinWatermark is the other half of the same CHECK. A watermark of three characters would
	// be a stamp nobody could trace anything back to.
	MinWatermark = 8
	// MaxParameterKeys bounds the filter blob. A screen sends a handful of filters; a hundred
	// of them is somebody using the column as storage.
	MaxParameterKeys = 32
	// MaxParameterValue bounds one filter value.
	MaxParameterValue = 200
	// MaxExportRows is the largest file this platform will render. It exists because the
	// worker holds the rendered bytes in memory before it stores them, and because an export
	// of two million rows is a database dump somebody is taking one CSV at a time.
	MaxExportRows = 50000
	// MaxStatementRows bounds one statement page. A provider's month is tens of invoices; a
	// hundred thousand of them is a period somebody typed wrongly.
	MaxStatementRows = 5000
	// MaxDifferences bounds what one run records. A run that found ten thousand differences
	// has found one systemic fault, and listing all of them would make the row unreadable
	// without making it more true.
	MaxDifferences = 500
	// MaxPeriodDays bounds a statement's or an export's period. Five years is longer than any
	// argument about an invoice and shorter than a request that would scan the whole table.
	MaxPeriodDays = 1827
)

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	// uuidPattern is what makes "no identifier" checkable. Every identifier in this platform
	// is a uuid, so one test catches a provider id, a claim id and a person id at once. It is
	// deliberately unanchored: an id hidden inside a longer string is still an id.
	uuidPattern = regexp.MustCompile(
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	// identifierKeyPattern names the keys an identifier hides behind. It asks for the
	// camel-case capital or the underscore rather than for the two letters, so `providerId`
	// and `claim_id` are refused and `valid`, `paid` and `overdue` are not.
	identifierKeyPattern = regexp.MustCompile(`^([A-Za-z0-9_]*(Id|_id)|id)$`)
	// parameterKeyPattern bounds what a filter may be called at all.
	parameterKeyPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,39}$`)
)

// ValidKind reports whether k is one of the five.
func ValidKind(k string) bool { return contains(Kinds, k) }

// ValidExportStatus reports whether s is one of the five.
func ValidExportStatus(s string) bool { return contains(ExportStatuses, s) }

// ValidRunStatus reports whether s is one of the three.
func ValidRunStatus(s string) bool { return contains(RunStatuses, s) }

// ValidScope reports whether s is TENANT or PROVIDER.
func ValidScope(s string) bool { return contains(Scopes, s) }

// SensitiveKind reports whether a kind needs `report.export.sensitive`. Exactly one does, and
// this is the single place that answer is computed: CLAIMS carries line descriptions, which is
// what a member was treated for written in words. Everything else is references, statuses and
// exact decimals — the same figures the caller could already read one page at a time.
func SensitiveKind(kind string) bool { return kind == KindClaims }

// Watermark builds the line stamped on every row and on the header of every file this platform
// hands out: who took it, out of which tenant, when, and which export row it was.
//
// It is one function because it is one string. A watermark built in the renderer and a
// watermark shown on the screen would be two strings that agree until somebody changes one.
func Watermark(tenantCode, requesterName string, at time.Time, exportID string) string {
	name := strings.TrimSpace(requesterName)
	if name == "" {
		// An export always has a requester; a display name is a column somebody may not have
		// filled in. The id is what makes the stamp traceable either way.
		name = "-"
	}
	mark := fmt.Sprintf("KAPSORA %s · %s · %s · %s",
		strings.TrimSpace(tenantCode), name, at.UTC().Format(time.RFC3339), exportID)
	if len(mark) > MaxWatermark {
		mark = string([]rune(mark)[:MaxWatermark])
	}
	return mark
}

// ValidateExportRequest checks everything about an export before a row exists for it.
//
// The format check is the one that says something the contract does not say twice: XLSX is a
// valid value of the column and is refused by this platform, because nothing here can write a
// spreadsheet. A caller that asks for one is told so rather than handed a CSV under another
// name.
func ValidateExportRequest(kind, format string, hasProvider bool,
	from, to *time.Time, currency string, parameters map[string]any,
) error {
	ve := &ValidationError{}
	if !ValidKind(kind) {
		ve.Add("kind", "ENUM", "geçerli bir dışa aktarma türü olmalı")
	}
	switch format {
	case FormatCSV:
	case FormatXLSX:
		ve.Add("format", "UNSUPPORTED",
			"bu sürümde yalnızca CSV üretilir; XLSX henüz desteklenmiyor")
	default:
		ve.Add("format", "ENUM", "geçerli bir dosya biçimi olmalı")
	}
	if kind == KindProviderStatement {
		if !hasProvider {
			ve.Add("providerOrganizationId", "REQUIRED", "cari ekstre için sağlayıcı verilmeli")
		}
		if from == nil || to == nil {
			ve.Add("periodFrom", "REQUIRED", "cari ekstre için dönem verilmeli")
		}
	}
	validatePeriod(ve, from, to)
	if currency != "" && !currencyPattern.MatchString(currency) {
		ve.Add("currencyCode", "FORMAT", "üç büyük harfli para birimi kodu olmalı")
	}
	validateParameters(ve, parameters)
	return ve.OrNil()
}

// ValidateStatementRequest checks a statement read before any query runs.
func ValidateStatementRequest(from, to *time.Time, currency string) error {
	ve := &ValidationError{}
	if from == nil || to == nil {
		ve.Add("periodFrom", "REQUIRED", "dönem başlangıcı ve bitişi verilmeli")
	}
	validatePeriod(ve, from, to)
	if currency != "" && !currencyPattern.MatchString(currency) {
		ve.Add("currencyCode", "FORMAT", "üç büyük harfli para birimi kodu olmalı")
	}
	return ve.OrNil()
}

func validatePeriod(ve *ValidationError, from, to *time.Time) {
	if from == nil || to == nil {
		return
	}
	if to.Before(*from) {
		ve.Add("periodTo", "RANGE", "dönem bitişi başlangıcından önce olamaz")
		return
	}
	if to.Sub(*from) > time.Duration(MaxPeriodDays)*24*time.Hour {
		ve.Add("periodTo", "RANGE",
			fmt.Sprintf("dönem en fazla %d gün olabilir", MaxPeriodDays))
	}
}

// ParametersAreAnonymous reports whether a filter blob is free of identifiers: no uuid in any
// value, and no key that names one. It is the Go half of the database's own function of the
// same name, and the two ask the same two questions on purpose.
func ParametersAreAnonymous(parameters map[string]any) bool {
	for key, value := range parameters {
		if identifierKeyPattern.MatchString(key) {
			return false
		}
		if uuidPattern.MatchString(fmt.Sprint(value)) {
			return false
		}
	}
	return true
}

// validateParameters turns the same two questions into field errors, so a caller learns which
// filter was refused rather than reading "validation failed".
func validateParameters(ve *ValidationError, parameters map[string]any) {
	if len(parameters) > MaxParameterKeys {
		ve.Add("parameters", "LENGTH",
			fmt.Sprintf("en fazla %d filtre alanı gönderilebilir", MaxParameterKeys))
		return
	}
	for key, value := range parameters {
		if !parameterKeyPattern.MatchString(key) {
			ve.Add("parameters."+key, "FORMAT", "filtre adı küçük harfle başlamalı")
			continue
		}
		if identifierKeyPattern.MatchString(key) {
			ve.Add("parameters."+key, "IDENTIFIER",
				"dışa aktarma filtreleri kimlik taşıyamaz; kapsamı ayrı alanlarda verin")
			continue
		}
		rendered := fmt.Sprint(value)
		if len(rendered) > MaxParameterValue {
			ve.Add("parameters."+key, "LENGTH",
				fmt.Sprintf("filtre değeri en fazla %d karakter olabilir", MaxParameterValue))
			continue
		}
		if uuidPattern.MatchString(rendered) {
			ve.Add("parameters."+key, "IDENTIFIER",
				"dışa aktarma filtreleri kimlik taşıyamaz; kapsamı ayrı alanlarda verin")
		}
	}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
