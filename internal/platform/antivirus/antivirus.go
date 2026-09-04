// Package antivirus is the malware scanning port of the document pipeline. A file from
// outside is the most likely way malware gets into the platform, so nothing reaches the
// secure bucket before something here has looked at it (WP-I4-04 section 2.2).
//
// The port is an interface for a reason that is not abstraction for its own sake: the
// pipeline's guarantee — an infected file leaves no bytes anywhere — has to be provable
// without a virus. A test drives a scanner that answers INFECTED on demand and asserts the
// whole clean-up; the ClamAV client below is then only responsible for turning a socket
// conversation into one of three answers.
package antivirus

import (
	"context"
	"errors"
	"io"
)

// Outcome mirrors document.scan_result.outcome.
type Outcome string

const (
	// OutcomeClean means the scanner looked at every byte and found nothing.
	OutcomeClean Outcome = "CLEAN"
	// OutcomeInfected means the scanner named something. The file is deleted and never
	// copied; the row survives so the incident is on record.
	OutcomeInfected Outcome = "INFECTED"
	// OutcomeError means the scanner could not reach a verdict. It is not "clean": a file
	// that cannot be scanned is never promoted.
	OutcomeError Outcome = "ERROR"
)

// Result is one verdict. Engine and SignatureVersion are recorded with it because "this
// file was clean" is only meaningful alongside what looked at it and how old its
// signatures were.
type Result struct {
	Outcome Outcome
	Engine  string
	// SignatureVersion is the scanner's signature database version, empty when the
	// scanner would not say.
	SignatureVersion string
	// Finding names what was found. It is set only for INFECTED and, sometimes, ERROR.
	Finding string
}

// ErrUnavailable is returned when the scanner could not be reached or did not answer.
// The pipeline treats it as retryable: the file stays in quarantine and stays unscanned.
var ErrUnavailable = errors.New("antivirus: scanner unavailable")

// Scanner reads a file body and returns a verdict. An implementation must never return
// OutcomeClean for bytes it did not read to the end.
type Scanner interface {
	Scan(ctx context.Context, body io.Reader) (Result, error)
	// Ping reports whether the scanner is answering; the readiness probe uses it.
	Ping(ctx context.Context) error
}
