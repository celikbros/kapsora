package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// GetDocument reads one document with its links. A document outside the caller's provider
// scope is not found rather than refused: that it exists at all is somebody else's
// business.
func (s *Service) GetDocument(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (Document, error) {
	var out Document
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		links, err := s.repo.ListLinks(ctx, tx, rc.TenantID, object.ID)
		if err != nil {
			return err
		}
		out = Document{Object: object, Links: links}
		return nil
	})
	if err != nil {
		return Document{}, err
	}
	return out, nil
}

// ListDocuments pages the tenant's documents, newest first.
func (s *Service) ListDocuments(ctx context.Context, rc identity.RequestContext, f Filter) (Page, error) {
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return Page{}, err
	}
	if f.ScanStatus != "" && !domain.ValidScanStatus(f.ScanStatus) {
		return Page{}, fieldError("scanStatus", "ENUM", "geçerli bir tarama durumu olmalı")
	}
	if f.Classification != "" && !domain.ValidClassification(f.Classification) {
		return Page{}, fieldError("classification", "ENUM", "geçerli bir gizlilik sınıfı olmalı")
	}

	var page Page
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListObjects(ctx, tx, rc.TenantID, ObjectQuery{
			Scope: scopeOf(rc), ScanStatus: f.ScanStatus, Classification: f.Classification,
			AggregateType: f.AggregateType, AggregateID: f.AggregateID,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			page.NextCursor = s.cursors.Encode(objectCursor(rows[len(rows)-1]))
		}
		page.Items = make([]Document, 0, len(rows))
		for _, object := range rows {
			links, err := s.repo.ListLinks(ctx, tx, rc.TenantID, object.ID)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, Document{Object: object, Links: links})
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// DownloadRequest is why somebody is opening a document. Both fields travel into the
// access event: "who read this" without "why" is not an answer a data protection review
// can use (v1.2 11.10).
type DownloadRequest struct {
	PurposeCode string
	ReasonText  string
}

// Download answers a short-lived presigned GET into the secure bucket, and only for a
// document that has been scanned clean. Every call writes an audit.access_event carrying
// the document's classification and the reason it was opened; a HEALTH-classified document
// writes a second, separate event, because who opened clinical material is a question
// asked on its own.
//
// The permission the link names is checked here rather than in the transport, because it
// is a property of the document: a clinical attachment is clinical wherever it is reached
// from, and a caller who may read documents in general is not thereby allowed this one.
func (s *Service) Download(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req DownloadRequest,
) (objectstore.PresignedURL, Document, error) {
	if err := domain.ValidateDownloadReason(req.ReasonText); err != nil {
		return objectstore.PresignedURL{}, Document{}, err
	}

	var (
		url objectstore.PresignedURL
		out Document
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		links, err := s.repo.ListLinks(ctx, tx, rc.TenantID, object.ID)
		if err != nil {
			return err
		}
		out = Document{Object: object, Links: links}

		if missing := missingLinkPermission(rc, links); missing != "" {
			return ErrLinkPermission
		}
		if err := downloadableOrError(object); err != nil {
			return err
		}

		url, err = s.store.PresignGet(ctx, s.storage.SecureBucket, object.ObjectKey, s.storage.DownloadTTL)
		if err != nil {
			return storeError("presign download", err)
		}
		if err := s.recordAccess(ctx, tx, rc, object, req, audit.OutcomeSuccess); err != nil {
			return err
		}
		if object.Classification == domain.ClassHealth {
			// The separate event v1.2 11.10 asks for. It is an ACCESS-category audit.event
			// rather than a second access_event so a health access report can be produced
			// without reading every download the tenant ever made.
			if err := s.audit.Record(ctx, tx, audit.Event{
				TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
				MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryAccess,
				ActionCode: "document.download.health", ResourceType: domain.AggregateType,
				ResourceID: nullUUID(object.ID), Outcome: audit.OutcomeSuccess,
				PurposeCode: req.PurposeCode,
				Detail: map[string]any{
					"classification": object.Classification,
					"link_count":     len(links),
				},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, ErrLinkPermission) {
		// A denied read of a clinical document is exactly the row a security review looks
		// for, so it is written in a transaction of its own: the refusal rolls the first
		// one back, and an audit row that rolls back with the thing it was auditing is an
		// audit row nobody ever sees.
		if auditErr := s.recordDenial(ctx, rc, out.Object, req); auditErr != nil {
			return objectstore.PresignedURL{}, Document{}, auditErr
		}
		return objectstore.PresignedURL{}, Document{}, err
	}
	if err != nil {
		return objectstore.PresignedURL{}, Document{}, err
	}
	return url, out, nil
}

// recordDenial writes the denied access event outside the rolled-back transaction.
func (s *Service) recordDenial(ctx context.Context, rc identity.RequestContext,
	object ObjectRecord, req DownloadRequest,
) error {
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		return s.recordAccess(ctx, tx, rc, object, req, audit.OutcomeDenied)
	})
}

// recordAccess writes the audit.access_event a download produces, whichever way it went.
func (s *Service) recordAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	object ObjectRecord, req DownloadRequest, outcome audit.Outcome,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		MembershipID: nullUUID(rc.MembershipID),
		ResourceType: domain.AggregateType, ResourceID: nullUUID(object.ID),
		AccessType: audit.AccessDownload, Classification: audit.Classification(object.Classification),
		PurposeCode: req.PurposeCode, ReasonText: req.ReasonText, Outcome: outcome,
	})
}

// missingLinkPermission returns the first permission a link requires that the caller does
// not hold, or "". Every link is checked rather than any one of them: a document that is a
// clinical attachment somewhere is a clinical attachment everywhere, and satisfying the
// laxest link would be a way around the strictest.
func missingLinkPermission(rc identity.RequestContext, links []LinkRecord) string {
	for _, link := range links {
		if link.RequiredPermission == nil || *link.RequiredPermission == "" {
			continue
		}
		if !rc.Has(*link.RequiredPermission) {
			return *link.RequiredPermission
		}
	}
	return ""
}

// downloadableOrError turns "not downloadable" into the one reason it is not. The three
// answers are deliberately different: a file still being scanned is a wait, an infected one
// is an incident, and a purged one is gone for good.
func downloadableOrError(object ObjectRecord) error {
	if domain.Downloadable(object.ScanStatus, object.Bucket, object.PurgedAt != nil) {
		return nil
	}
	switch {
	case object.ScanStatus == domain.ScanInfected:
		return ErrInfected
	case object.PurgedAt != nil:
		return ErrPurged
	default:
		return ErrNotScanned
	}
}

// ScanHistory returns the verdicts recorded about a document, newest first. It is what an
// operator reads when a file will not download.
func (s *Service) ScanHistory(ctx context.Context, rc identity.RequestContext, id uuid.UUID) ([]ScanResultRecord, error) {
	var out []ScanResultRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		out, err = s.repo.ListScanResults(ctx, tx, rc.TenantID, object.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
