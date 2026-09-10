// Package reportgw adapts the modules the report package depends on to the narrow ports it
// declares. There is one of them: WP-I4-04's document store, which holds every export file.
//
// It is a gateway rather than a direct call for the ordinary reason — the document service opens
// transactions of its own — and for one that matters here. The document module writes the access
// event a *file* download produces; the report module writes the one an *export* download
// produces. Two rows for one act, each answering a question the other cannot, and the seam is
// what keeps them from becoming one row that answers neither well.
package reportgw

import (
	"context"

	"github.com/google/uuid"

	documentapp "github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/report/application"
)

// Documents implements application.Documents on the document service.
type Documents struct {
	svc *documentapp.Service
}

// NewDocuments wires the gateway.
func NewDocuments(svc *documentapp.Service) *Documents { return &Documents{svc: svc} }

var _ application.Documents = (*Documents)(nil)

// exportDocumentType is the `document.link.document_type_code` every export file is linked under.
// It is what makes an export findable from the document side: "show me every export this tenant
// produced in March" is a question the document list answers, and it answers it by this code.
const exportDocumentType = "EXPORT"

// exportPurpose is the link's purpose. It says in one line what the file is for, which is what a
// person reading the document's own screen sees instead of a uuid.
const exportPurpose = "Rapor dışa aktarma dosyası"

// Store implements application.Documents. The file goes into the secure bucket with a CLEAN
// verdict this platform signed itself, and it is linked to the export that produced it — so the
// document and the row that owns it can each be found from the other.
func (d *Documents) Store(ctx context.Context, tenantID uuid.UUID, in application.RenderedFile,
) (uuid.UUID, error) {
	doc, err := d.svc.StoreRendered(ctx, tenantID, documentapp.RenderedFile{
		Filename: in.Filename, ContentType: in.ContentType,
		Classification: in.Classification, Body: in.Body,
		OwnerOrganizationID: in.OwnerOrganizationID,
		Action:              "report.export.store",
		Detail: map[string]any{
			"export_kind":     in.Kind,
			"retain_hours":    int(in.RetainFor.Hours()),
			"export_row_size": len(in.Body),
		},
	})
	if err != nil {
		return uuid.Nil, err
	}
	// The link carries the permission the file needs, which is what makes WP-I4-04's own
	// download refuse an export to somebody who may read documents in general but may not take
	// numbers out of the building. The export's own download checks it too; this is the half
	// that holds when the file is reached from the document screen instead.
	if _, err := d.svc.LinkDocument(ctx, systemContext(tenantID), doc.Object.ID,
		documentapp.NewLinkInput{
			AggregateType: exportAggregate, AggregateID: in.ExportID,
			DocumentTypeCode: exportDocumentType, Purpose: exportPurpose,
			RequiredPermission: application.PermissionExport,
		}); err != nil {
		return uuid.Nil, err
	}
	return doc.Object.ID, nil
}

// Download implements application.Documents.
func (d *Documents) Download(ctx context.Context, rc identity.RequestContext, documentID uuid.UUID,
	purposeCode, reasonText string,
) (objectstore.PresignedURL, error) {
	url, _, err := d.svc.Download(ctx, rc, documentID, documentapp.DownloadRequest{
		PurposeCode: purposeCode, ReasonText: reasonText,
	})
	return url, err
}

// Purge implements application.Documents.
func (d *Documents) Purge(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error) {
	return d.svc.PurgeObject(ctx, tenantID, documentID)
}

// exportAggregate is the aggregate type an export file's link points at.
const exportAggregate = "EXPORT"

// systemContext is the caller a worker acts as: a tenant and nothing else. The link is written by
// the process that rendered the file, and attributing it to the person who asked for the export
// would say they did something they did not.
func systemContext(tenantID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{TenantID: tenantID}
}
