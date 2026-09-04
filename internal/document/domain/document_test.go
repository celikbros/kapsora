package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/document/domain"
)

// TestDownloadableIsTheOneAnswer walks every combination of the three things that decide
// whether a file may be handed out. It is exhaustive on purpose: this function is the only
// place the product answers "may somebody have this file", and a combination it got wrong
// would be a file handed out that nobody scanned.
func TestDownloadableIsTheOneAnswer(t *testing.T) {
	buckets := []string{domain.BucketQuarantine, domain.BucketSecure}
	for _, status := range domain.ScanStatuses {
		for _, bucket := range buckets {
			for _, purged := range []bool{false, true} {
				got := domain.Downloadable(status, bucket, purged)
				want := status == domain.ScanClean && bucket == domain.BucketSecure && !purged
				if got != want {
					t.Fatalf("Downloadable(%s, %s, purged=%t) = %t, want %t",
						status, bucket, purged, got, want)
				}
			}
		}
	}
	// The three that matter, spelled out, so a change to the rule fails here by name.
	if !domain.Downloadable(domain.ScanClean, domain.BucketSecure, false) {
		t.Fatal("a clean file in the secure bucket must be downloadable")
	}
	if domain.Downloadable(domain.ScanInfected, domain.BucketSecure, false) {
		t.Fatal("an infected file must never be downloadable, whatever bucket a row claims")
	}
	if domain.Downloadable(domain.ScanFailed, domain.BucketQuarantine, false) {
		t.Fatal("a file that could not be scanned must never be downloadable")
	}
	if domain.Downloadable(domain.ScanClean, domain.BucketSecure, true) {
		t.Fatal("a purged file has no bytes left to hand out")
	}
}

// TestNormalizeFilenameKeepsOnlyTheName: the filename is a label shown to a person and is
// never used to build a key, so a browser that sends a path is answered with the bare name
// rather than a rejection.
func TestNormalizeFilenameKeepsOnlyTheName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"fatura.pdf", "fatura.pdf"},
		{"  rapor.pdf  ", "rapor.pdf"},
		{`..\..\etc\passwd`, "passwd"},
		{"/var/tmp/../secret.key", "secret.key"},
		{"C:\\Users\\alice\\epikriz.pdf", "epikriz.pdf"},
		{"a/b/", ""},
		{"", ""},
	} {
		if got := domain.NormalizeFilename(c.in); got != c.want {
			t.Fatalf("NormalizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestValidateUploadRefusesWhatCannotBeStored covers each field the API can check before a
// single byte exists.
func TestValidateUploadRefusesWhatCannotBeStored(t *testing.T) {
	valid := func() (string, string, string, string, int64) {
		return "fatura.pdf", "application/pdf", domain.ClassInternal, "", 1024
	}
	if err := domain.ValidateUpload(valid()); err != nil {
		t.Fatalf("a valid upload was refused: %v", err)
	}

	for _, c := range []struct {
		what      string
		filename  string
		mediaType string
		class     string
		digest    string
		size      int64
		field     string
	}{
		{"no filename", "   ", "application/pdf", domain.ClassInternal, "", 10, "originalFilename"},
		{"a path only", `a/b/`, "application/pdf", domain.ClassInternal, "", 10, "originalFilename"},
		{"a filename that is too long", strings.Repeat("a", 300), "application/pdf", domain.ClassInternal, "", 10, "originalFilename"},
		{"no media type", "a.pdf", "pdf", domain.ClassInternal, "", 10, "contentType"},
		{"a media type with a space", "a.pdf", "application/ pdf", domain.ClassInternal, "", 10, "contentType"},
		{"an unknown classification", "a.pdf", "application/pdf", "TOP_SECRET", "", 10, "classification"},
		{"an empty file", "a.pdf", "application/pdf", domain.ClassInternal, "", 0, "byteSize"},
		{"a file over the ceiling", "a.pdf", "application/pdf", domain.ClassInternal, "", domain.MaxByteSize + 1, "byteSize"},
		{"an uppercase digest", "a.pdf", "application/pdf", domain.ClassInternal, strings.Repeat("A", 64), 10, "sha256"},
		{"a short digest", "a.pdf", "application/pdf", domain.ClassInternal, strings.Repeat("a", 63), 10, "sha256"},
	} {
		err := domain.ValidateUpload(c.filename, c.mediaType, c.class, c.digest, c.size)
		assertFieldError(t, err, c.field, c.what)
	}
}

// TestValidateCompleteInsistsOnADigest: the claim is not trusted, but it has to be a claim
// about something. A completeUpload with no digest would be a scan queued over nothing.
func TestValidateCompleteInsistsOnADigest(t *testing.T) {
	if err := domain.ValidateComplete(strings.Repeat("a", 64), 10); err != nil {
		t.Fatalf("a valid completion was refused: %v", err)
	}
	assertFieldError(t, domain.ValidateComplete("", 10), "sha256", "no digest")
	assertFieldError(t, domain.ValidateComplete(strings.Repeat("a", 64), 0), "byteSize", "an empty file")
}

// TestValidateLinkChecksTheCodesAndThePermission.
func TestValidateLinkChecksTheCodesAndThePermission(t *testing.T) {
	if err := domain.ValidateLink("SERVICE_REQUEST", "INVOICE", "fatura", "health.clinical.read"); err != nil {
		t.Fatalf("a valid link was refused: %v", err)
	}
	assertFieldError(t, domain.ValidateLink("service_request", "INVOICE", "", ""), "aggregateType", "a lowercase aggregate type")
	assertFieldError(t, domain.ValidateLink("SERVICE_REQUEST", "invoice", "", ""), "documentTypeCode", "a lowercase document type")
	assertFieldError(t, domain.ValidateLink("SERVICE_REQUEST", "INVOICE", strings.Repeat("x", 300), ""), "purpose", "an over-long purpose")
	assertFieldError(t, domain.ValidateLink("SERVICE_REQUEST", "INVOICE", "", "Health Clinical Read"), "requiredPermission", "a permission that is not a code")
	// A single-segment permission is not one either: every code in the catalogue is dotted.
	assertFieldError(t, domain.ValidateLink("SERVICE_REQUEST", "INVOICE", "", "document"), "requiredPermission", "an undotted permission")
}

// TestValidateLegalHoldRefusesAHoldOverNothing: a hold that names nothing would look like
// protection and protect nothing, which is worse than no hold at all.
func TestValidateLegalHoldRefusesAHoldOverNothing(t *testing.T) {
	if err := domain.ValidateLegalHold("dava", "", true, false, false); err != nil {
		t.Fatalf("a hold over a document was refused: %v", err)
	}
	if err := domain.ValidateLegalHold("dava", "SERVICE_REQUEST", false, false, true); err != nil {
		t.Fatalf("a hold over a record was refused: %v", err)
	}
	assertFieldError(t, domain.ValidateLegalHold("dava", "", false, false, false), "objectId", "a hold over nothing")
	assertFieldError(t, domain.ValidateLegalHold("", "", true, false, false), "reason", "a hold with no reason")
	assertFieldError(t, domain.ValidateLegalHold("dava", "service_request", false, false, true), "aggregateType", "a lowercase aggregate type")
}

func assertFieldError(t *testing.T, err error, field, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted", what)
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("%s produced %v, want a validation error", what, err)
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("%s produced %v, want *ValidationError", what, err)
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return
		}
	}
	t.Fatalf("%s named %+v, want a failure on %s", what, ve.Fields, field)
}
