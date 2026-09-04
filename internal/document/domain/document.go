// Package domain holds what a document may look like and how its scan status may move:
// the closed lists the schema repeats as CHECK constraints, the one path a file takes from
// arriving to being readable, and the validation of everything a caller may send. It
// depends on nothing outside the standard library, so every rule here is unit-testable
// without a database, an object store or a virus scanner.
//
// The rule the whole package is built around is stated once, here, as a function:
// Downloadable. A file is readable only after something looked at every byte of it and
// said so. Everything else — the bucket it sits in, the status of the row, the key that
// was handed out — follows from that one answer, and there is deliberately no second place
// in the codebase that decides it.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	return fmt.Sprintf("document: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("document: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Scan statuses (migration 000028). They are the life of a file in one column.
const (
	// ScanPending is a reserved row with an upload URL and, as yet, no bytes.
	ScanPending = "PENDING"
	// ScanScanning is bytes in quarantine waiting for, or in front of, the scanner.
	ScanScanning = "SCANNING"
	// ScanClean is the only status whose bytes are in the secure bucket.
	ScanClean = "CLEAN"
	// ScanInfected is a file the scanner named something in. Its bytes are gone; the row
	// stays so the incident is on record.
	ScanInfected = "INFECTED"
	// ScanFailed is a file the scanner could not reach a verdict about. It is not clean:
	// its bytes stay in quarantine and it is never promoted.
	ScanFailed = "FAILED"
)

// Buckets. There are exactly two, and which one an object is in is the difference between
// "nobody has looked at this yet" and "this is safe to hand out".
const (
	BucketQuarantine = "quarantine"
	BucketSecure     = "secure"
)

// Data classifications (audit.access_event.data_classification and the object's own
// column). HEALTH is the one that changes what a download costs: it writes an access event
// of its own (v1.2 11.10).
const (
	ClassInternal     = "INTERNAL"
	ClassConfidential = "CONFIDENTIAL"
	ClassPersonal     = "PERSONAL"
	ClassHealth       = "HEALTH"
)

// AggregateType is what a document's own events are recorded under.
const AggregateType = "DOCUMENT"

// Closed lists the database repeats as CHECK constraints.
var (
	ScanStatuses    = []string{ScanPending, ScanScanning, ScanClean, ScanInfected, ScanFailed}
	Classifications = []string{ClassInternal, ClassConfidential, ClassPersonal, ClassHealth}
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxFilename bounds the name the client uploaded the file under. It is stored
	// because a person needs to recognise their own document; it never reaches an audit
	// detail, where a key holding a name is dropped anyway (internal/audit/meta.go).
	MaxFilename = 255
	// MaxContentType bounds the declared media type.
	MaxContentType = 255
	// MaxPurpose bounds why a link exists.
	MaxPurpose = 200
	// MaxReason bounds the reason a legal hold was placed and the reason a download was
	// asked for.
	MaxReason = 1000
	// MaxByteSize is the largest upload the contract accepts, 100 MiB. It is signed into
	// the upload URL, which is where the limit actually holds: the API has no body to
	// measure, so a number checked only here would be a number nobody enforces.
	MaxByteSize = 100 << 20
	// MinByteSize refuses an empty upload. A zero-byte document is a mistake every time.
	MinByteSize = 1
)

var (
	codePattern       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)
	// contentTypePattern is deliberately permissive about parameters and strict about
	// shape: the media type is stored and handed back to the browser, never interpreted.
	contentTypePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9!#$&^_.+-]{0,126}/[a-zA-Z0-9][a-zA-Z0-9!#$&^_.+-]{0,126}$`)
	hexPattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Downloadable reports whether an object may be handed to somebody. This is the single
// place that answer is computed. A file that has not been scanned, whose scan failed, that
// the scanner named something in, or whose bytes retention has already removed, is not
// downloadable — and neither is one that somehow sits outside the secure bucket.
func Downloadable(scanStatus, bucket string, purged bool) bool {
	return scanStatus == ScanClean && bucket == BucketSecure && !purged
}

// ValidClassification reports whether c is one of the four.
func ValidClassification(c string) bool { return contains(Classifications, c) }

// ValidScanStatus reports whether s is one of the five.
func ValidScanStatus(s string) bool { return contains(ScanStatuses, s) }

// NormalizeFilename trims the name and strips any directory part a browser sent with it.
// A file called `..\..\etc\passwd` is stored as `passwd`: the name is a label shown to a
// person, and it is never used to build a key or a path.
func NormalizeFilename(name string) string {
	trimmed := strings.TrimSpace(name)
	if idx := strings.LastIndexAny(trimmed, `/\`); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.TrimSpace(trimmed)
}

// ValidateUpload checks everything the API can check about an upload before any byte
// exists. The size is checked here as well as signed into the URL: a client that is told
// its file is too large before it spends five minutes sending it is a better client.
func ValidateUpload(filename, contentType, classification, sha256Hex string, byteSize int64) error {
	ve := &ValidationError{}
	if name := NormalizeFilename(filename); name == "" || len(name) > MaxFilename {
		ve.Add("originalFilename", "LENGTH",
			fmt.Sprintf("dosya adı 1 ile %d karakter arasında olmalı", MaxFilename))
	}
	if !contentTypePattern.MatchString(contentType) || len(contentType) > MaxContentType {
		ve.Add("contentType", "FORMAT", "tip/alt-tip biçiminde bir medya türü olmalı")
	}
	if !ValidClassification(classification) {
		ve.Add("classification", "ENUM", "geçerli bir gizlilik sınıfı olmalı")
	}
	if byteSize < MinByteSize || byteSize > MaxByteSize {
		ve.Add("byteSize", "RANGE",
			fmt.Sprintf("dosya boyutu %d ile %d bayt arasında olmalı", MinByteSize, MaxByteSize))
	}
	if sha256Hex != "" && !hexPattern.MatchString(sha256Hex) {
		ve.Add("sha256", "FORMAT", "64 karakterlik küçük harf onaltılık özet olmalı")
	}
	return ve.OrNil()
}

// ValidateComplete checks what the client claims about bytes the API never saw. Neither
// value is trusted: the worker recomputes the digest from the bytes it streams to the
// scanner, and it is that digest the object is finally stored under.
func ValidateComplete(sha256Hex string, byteSize int64) error {
	ve := &ValidationError{}
	if !hexPattern.MatchString(sha256Hex) {
		ve.Add("sha256", "FORMAT", "64 karakterlik küçük harf onaltılık özet olmalı")
	}
	if byteSize < MinByteSize || byteSize > MaxByteSize {
		ve.Add("byteSize", "RANGE",
			fmt.Sprintf("dosya boyutu %d ile %d bayt arasında olmalı", MinByteSize, MaxByteSize))
	}
	return ve.OrNil()
}

// ValidateLink checks a link before it is written.
func ValidateLink(aggregateType, documentTypeCode, purpose, requiredPermission string) error {
	ve := &ValidationError{}
	if !codePattern.MatchString(aggregateType) {
		ve.Add("aggregateType", "FORMAT", "BÜYÜK_HARF kodu olmalı")
	}
	if !codePattern.MatchString(documentTypeCode) {
		ve.Add("documentTypeCode", "FORMAT", "BÜYÜK_HARF kodu olmalı")
	}
	if purpose != "" && len(purpose) > MaxPurpose {
		ve.Add("purpose", "LENGTH", fmt.Sprintf("en fazla %d karakter olmalı", MaxPurpose))
	}
	if requiredPermission != "" && !permissionPattern.MatchString(requiredPermission) {
		ve.Add("requiredPermission", "FORMAT", "nokta ile ayrılmış küçük harf yetki kodu olmalı")
	}
	return ve.OrNil()
}

// ValidateLegalHold checks a hold before it is placed. A hold that names nothing would
// look like protection and protect nothing, so it is refused here as well as by the CHECK
// the schema carries.
func ValidateLegalHold(reason, aggregateType string, hasObject, hasPerson, hasAggregate bool) error {
	ve := &ValidationError{}
	if r := strings.TrimSpace(reason); r == "" || len(r) > MaxReason {
		ve.Add("reason", "LENGTH", fmt.Sprintf("gerekçe 1 ile %d karakter arasında olmalı", MaxReason))
	}
	if !hasObject && !hasPerson && !hasAggregate {
		ve.Add("objectId", "REQUIRED", "belge, kişi ya da kayıt hedeflerinden biri verilmeli")
	}
	if hasAggregate && !codePattern.MatchString(aggregateType) {
		ve.Add("aggregateType", "FORMAT", "BÜYÜK_HARF kodu olmalı")
	}
	return ve.OrNil()
}

// ValidateDownloadReason checks the reason a download carries into the access event.
func ValidateDownloadReason(reasonText string) error {
	if len(reasonText) > MaxReason {
		ve := &ValidationError{}
		ve.Add("reasonText", "LENGTH", fmt.Sprintf("en fazla %d karakter olmalı", MaxReason))
		return ve
	}
	return nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
