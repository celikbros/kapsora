package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// ErrNoScanner is a process asked to scan without a scanner configured. It is an error
// rather than a skip on purpose: the one thing this pipeline must never do is treat the
// absence of a verdict as a clean one.
var ErrNoScanner = errors.New("document: no malware scanner is configured")

// ScanReport is what one scan did, for the job log and for tests.
type ScanReport struct {
	ObjectID uuid.UUID
	Outcome  antivirus.Outcome
	// Promoted is true when the bytes were copied into the secure bucket by this run.
	Promoted bool
	// Deduplicated is true when the same bytes were already stored and this object was
	// pointed at the copy that exists rather than at a second one.
	Deduplicated bool
	Finding      string
}

// HandleScanRequested is the outbox handler registered on kapsora-worker. It is safe to
// redeliver: a decided object is re-tidied rather than re-scanned, and an undecided one is
// scanned again from the bytes that are still in quarantine.
func (s *Service) HandleScanRequested(ctx context.Context, d outbox.Delivery) error {
	if !d.TenantID.Valid || d.TenantID.UUID == uuid.Nil {
		return outbox.Permanent(fmt.Errorf("document: scan event %s has no tenant", d.ID))
	}
	if d.AggregateID == uuid.Nil {
		return outbox.Permanent(fmt.Errorf("document: scan event %s has no document id", d.ID))
	}
	_, err := s.ScanObject(ctx, d.TenantID.UUID, d.AggregateID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrObjectNotFound), errors.Is(err, ErrNoScanner):
		return outbox.Permanent(err)
	default:
		// Everything else — the store, the scanner, the database — is worth another go.
		// The file stays in quarantine and stays unreadable until one of them succeeds.
		return err
	}
}

// ScanObject is the whole pipeline for one file: read it out of quarantine, hash it and
// feed it to the scanner in the same pass, record the verdict, and act on it.
//
// The order of operations is the safety property, so it is written out here rather than
// left to be inferred:
//
//   - Nothing is copied to the secure bucket before a CLEAN verdict exists. There is one
//     Copy call in this package and it is inside the CLEAN branch.
//   - The verdict is committed before the quarantine copy is deleted. A crash in between
//     leaves bytes in a bucket nobody can read from and a redelivery that finishes the
//     tidy-up; the other order would lose the record of an incident that did happen.
//   - An INFECTED file has its secure key deleted too. It should never have one — nothing
//     copied it — and asking anyway is what turns "we never copy" from a claim about the
//     code into a fact about the bucket.
//   - The digest stored is the one computed here, from the bytes that were scanned, never
//     the one the client claimed about bytes the API never saw.
func (s *Service) ScanObject(ctx context.Context, tenantID, objectID uuid.UUID) (ScanReport, error) {
	if s.scanner == nil {
		return ScanReport{}, ErrNoScanner
	}
	object, version, err := s.loadForScan(ctx, tenantID, objectID)
	if err != nil {
		return ScanReport{}, err
	}
	// A decided object needs no second verdict; it may still need its quarantine copy
	// removed, which is exactly what a redelivered event is for.
	if object.ScanStatus == domain.ScanClean || object.ScanStatus == domain.ScanInfected {
		return ScanReport{ObjectID: objectID, Outcome: outcomeOf(object.ScanStatus)},
			s.cleanUpQuarantine(ctx, object)
	}
	if object.ScanStatus == domain.ScanPending {
		return ScanReport{}, fmt.Errorf("%w: %s has not finished uploading", ErrObjectNotFound, objectID)
	}

	result, digest, size, err := s.scanQuarantined(ctx, object)
	if err != nil {
		// The verdict could not be reached. Recording the attempt and leaving the object
		// FAILED is what stops it being promoted; the error travels on so the worker
		// retries with backoff.
		if recErr := s.recordScanFailure(ctx, tenantID, object, version, result, err); recErr != nil {
			return ScanReport{}, recErr
		}
		return ScanReport{ObjectID: objectID, Outcome: antivirus.OutcomeError, Finding: result.Finding}, err
	}

	switch result.Outcome {
	case antivirus.OutcomeInfected:
		return s.quarantineInfected(ctx, tenantID, object, version, result, digest, size)
	case antivirus.OutcomeClean:
		return s.promoteClean(ctx, tenantID, object, version, result, digest, size)
	default:
		if recErr := s.recordScanFailure(ctx, tenantID, object, version, result, nil); recErr != nil {
			return ScanReport{}, recErr
		}
		return ScanReport{ObjectID: objectID, Outcome: antivirus.OutcomeError, Finding: result.Finding},
			fmt.Errorf("document: scanner returned no verdict for %s: %s", objectID, result.Finding)
	}
}

// loadForScan reads the object and the version the verdict will be recorded against.
func (s *Service) loadForScan(ctx context.Context, tenantID, objectID uuid.UUID) (ObjectRecord, VersionRecord, error) {
	var (
		object  ObjectRecord
		version VersionRecord
	)
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if object, err = s.repo.LockObject(ctx, tx, tenantID, objectID); err != nil {
			return err
		}
		if object.ScanStatus == domain.ScanPending {
			return nil
		}
		version, err = s.repo.LatestVersion(ctx, tx, tenantID, objectID)
		return err
	})
	return object, version, err
}

// scanQuarantined streams the object out of quarantine once, hashing it and feeding the
// scanner from the same pass. One read: a second one could see different bytes, and the
// digest would then describe a file nobody scanned.
func (s *Service) scanQuarantined(ctx context.Context, object ObjectRecord) (antivirus.Result, []byte, int64, error) {
	body, err := s.store.Open(ctx, s.storage.QuarantineBucket, object.ObjectKey)
	if err != nil {
		return antivirus.Result{Outcome: antivirus.OutcomeError}, nil, 0,
			storeError("open quarantined object", err)
	}
	defer func() { _ = body.Close() }()

	digest := sha256.New()
	counter := &countingReader{inner: io.TeeReader(body, digest)}
	result, err := s.scanner.Scan(ctx, counter)
	if err != nil {
		return result, nil, 0, err
	}
	return result, digest.Sum(nil), counter.count, nil
}

// promoteClean copies the bytes into the secure bucket and records the CLEAN verdict, or,
// when the same bytes are already stored, points this object at the copy that exists.
func (s *Service) promoteClean(ctx context.Context, tenantID uuid.UUID, object ObjectRecord,
	version VersionRecord, result antivirus.Result, digest []byte, size int64,
) (ScanReport, error) {
	report := ScanReport{ObjectID: object.ID, Outcome: antivirus.OutcomeClean}

	var canonical *ObjectRecord
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		existing, err := s.repo.FindCleanByDigest(ctx, tx, tenantID, digest, Scope{})
		switch {
		case err == nil && existing.ID != object.ID:
			canonical = &existing
		case err == nil, errors.Is(err, ErrObjectNotFound):
		default:
			return err
		}
		return nil
	})
	if err != nil {
		return report, err
	}

	// The one copy in this package, and it is downstream of a CLEAN verdict. A duplicate
	// is not copied at all: its bytes are already in the secure bucket under the canonical
	// key, and writing them again would be a second copy of one file.
	if canonical == nil {
		if err := s.store.Copy(ctx, s.storage.QuarantineBucket, object.ObjectKey,
			s.storage.SecureBucket, object.ObjectKey); err != nil {
			return report, storeError("promote to secure", err)
		}
	}

	err = s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.CreateScanResult(ctx, tx, tenantID, NewScanResultRow{
			ObjectID: object.ID, VersionID: version.ID, Engine: engineOf(result),
			SignatureVersion: optionalPtr(result.SignatureVersion), Outcome: string(antivirus.OutcomeClean),
		}); err != nil {
			return err
		}
		if canonical != nil {
			report.Deduplicated = true
			return s.repo.MarkDuplicate(ctx, tx, tenantID, object.ID, canonical.ID,
				canonical.ObjectKey, digest, size)
		}
		promoted, err := s.repo.Promote(ctx, tx, tenantID, PromotionRow{
			ObjectID: object.ID, SHA256: digest, ByteSize: size, SecureKey: object.ObjectKey,
		})
		if err != nil {
			return err
		}
		if !promoted {
			// Somebody else stored these exact bytes between the lookup above and this
			// statement. The unique index refused the second canonical row, which is the
			// answer: this object is a pointer at theirs.
			return errPromotionRaced
		}
		report.Promoted = true
		return nil
	})
	if errors.Is(err, errPromotionRaced) {
		return s.resolveRace(ctx, tenantID, object, digest, size)
	}
	if err != nil {
		return report, err
	}

	// Last, and only now: the quarantine copy. Doing it before the commit would risk a
	// clean file with no bytes anywhere if the transaction then failed.
	if err := s.cleanUpQuarantine(ctx, object); err != nil {
		return report, err
	}
	// A digest the client claimed and then did not deliver is worth knowing about even
	// when the file turned out clean, so it is recorded rather than silently overwritten.
	if err := s.recordPromotion(ctx, tenantID, object, digest, size, report); err != nil {
		return report, err
	}
	return report, nil
}

// errPromotionRaced marks the unique index refusing a second canonical row for one digest.
var errPromotionRaced = errors.New("document: another object already stores these bytes")

// resolveRace points an object at the canonical copy of the same bytes after losing the
// promotion race, and removes the bytes it uploaded of its own.
func (s *Service) resolveRace(ctx context.Context, tenantID uuid.UUID, object ObjectRecord,
	digest []byte, size int64,
) (ScanReport, error) {
	report := ScanReport{ObjectID: object.ID, Outcome: antivirus.OutcomeClean, Deduplicated: true}
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		canonical, err := s.repo.FindCleanByDigest(ctx, tx, tenantID, digest, Scope{})
		if err != nil {
			return err
		}
		return s.repo.MarkDuplicate(ctx, tx, tenantID, object.ID, canonical.ID,
			canonical.ObjectKey, digest, size)
	})
	if err != nil {
		return report, err
	}
	// The loser's own copy in the secure bucket, if the Copy above got that far, is now
	// referenced by nothing: the row points at the canonical key.
	if err := s.store.Remove(ctx, s.storage.SecureBucket, object.ObjectKey); err != nil {
		return report, storeError("remove duplicate secure copy", err)
	}
	if err := s.cleanUpQuarantine(ctx, object); err != nil {
		return report, err
	}
	return report, nil
}

// quarantineInfected records the incident and makes sure the file exists nowhere.
func (s *Service) quarantineInfected(ctx context.Context, tenantID uuid.UUID, object ObjectRecord,
	version VersionRecord, result antivirus.Result, digest []byte, size int64,
) (ScanReport, error) {
	report := ScanReport{ObjectID: object.ID, Outcome: antivirus.OutcomeInfected, Finding: result.Finding}

	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.CreateScanResult(ctx, tx, tenantID, NewScanResultRow{
			ObjectID: object.ID, VersionID: version.ID, Engine: engineOf(result),
			SignatureVersion: optionalPtr(result.SignatureVersion),
			Outcome:          string(antivirus.OutcomeInfected), Finding: optionalPtr(result.Finding),
		}); err != nil {
			return err
		}
		if err := s.repo.MarkInfected(ctx, tx, tenantID, object.ID, digest, size); err != nil {
			return err
		}
		// SECURITY rather than BUSINESS: this is a row a security review reads, not one a
		// report groups by. The finding is a signature name, which is a code rather than
		// anything about a person.
		return s.recordSecurity(ctx, tx, tenantID, "document.scan.infected", object.ID,
			map[string]any{
				"finding": result.Finding, "engine": engineOf(result),
				"signature_version": result.SignatureVersion,
				"byte_size":         size, "classification": object.Classification,
			})
	})
	if err != nil {
		return report, err
	}

	if err := s.cleanUpQuarantine(ctx, object); err != nil {
		return report, err
	}
	// Nothing ever copied this file into the secure bucket, and asking the bucket to
	// delete a key it does not have is free. It is here so "an infected file has no bytes
	// anywhere" is a property of the bucket rather than of a code path being read
	// correctly.
	if err := s.store.Remove(ctx, s.storage.SecureBucket, object.ObjectKey); err != nil {
		return report, storeError("remove infected secure key", err)
	}
	return report, nil
}

// recordScanFailure writes an ERROR verdict and leaves the object FAILED. A file that
// cannot be scanned is never promoted, so this path never touches a bucket.
func (s *Service) recordScanFailure(ctx context.Context, tenantID uuid.UUID, object ObjectRecord,
	version VersionRecord, result antivirus.Result, cause error,
) error {
	finding := result.Finding
	if finding == "" && cause != nil {
		finding = truncate(cause.Error(), 500)
	}
	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if version.ID != uuid.Nil {
			if _, err := s.repo.CreateScanResult(ctx, tx, tenantID, NewScanResultRow{
				ObjectID: object.ID, VersionID: version.ID, Engine: engineOf(result),
				SignatureVersion: optionalPtr(result.SignatureVersion),
				Outcome:          string(antivirus.OutcomeError), Finding: optionalPtr(finding),
			}); err != nil {
				return err
			}
		}
		return s.repo.MarkFailed(ctx, tx, tenantID, object.ID)
	})
}

// recordPromotion writes the business audit row for a file that became readable.
func (s *Service) recordPromotion(ctx context.Context, tenantID uuid.UUID, object ObjectRecord,
	digest []byte, size int64, report ScanReport,
) error {
	claimedMatches := object.SHA256 != nil && hex.EncodeToString(object.SHA256) == hex.EncodeToString(digest)
	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return s.audit.Record(ctx, tx, auditEvent(tenantID, object.ID, "document.scan.clean",
			map[string]any{
				"byte_size": size, "classification": object.Classification,
				"deduplicated": report.Deduplicated,
				// Whether the digest the client claimed matches the bytes that arrived. A
				// false here is not a failure — the stored digest is the computed one
				// either way — but it is the trace of a client saying one thing and
				// sending another.
				"claimed_digest_matches": claimedMatches,
			}))
	})
}

// cleanUpQuarantine removes the quarantine copy. Deleting a key that is already gone
// succeeds, so a redelivered event finishes what a crashed one started.
func (s *Service) cleanUpQuarantine(ctx context.Context, object ObjectRecord) error {
	if err := s.store.Remove(ctx, s.storage.QuarantineBucket, object.ObjectKey); err != nil {
		return storeError("remove quarantined object", err)
	}
	return nil
}

// countingReader counts the bytes that actually reached the scanner. The size stored is
// the counted one, not the one the client claimed.
type countingReader struct {
	inner io.Reader
	count int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	r.count += int64(n)
	return n, err
}

func engineOf(result antivirus.Result) string {
	if result.Engine == "" {
		return "unknown"
	}
	return result.Engine
}

func outcomeOf(scanStatus string) antivirus.Outcome {
	if scanStatus == domain.ScanInfected {
		return antivirus.OutcomeInfected
	}
	return antivirus.OutcomeClean
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
