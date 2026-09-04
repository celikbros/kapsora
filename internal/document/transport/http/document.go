package documenthttp

import (
	"encoding/hex"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/document/domain"
)

// ListDocuments implements listDocuments.
func (h *Handler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.Filter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		ScanStatus:     r.URL.Query().Get("scanStatus"),
		Classification: r.URL.Query().Get("classification"),
		AggregateType:  r.URL.Query().Get("aggregateType"),
		AggregateID:    queryUUID(r, "aggregateId", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListDocuments(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.DocumentPage{Items: make([]kapsorav1.Document, 0, len(page.Items))}
	for _, doc := range page.Items {
		out.Items = append(out.Items, documentView(doc))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateUpload implements createUpload.
func (h *Handler) CreateUpload(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionUpload)
	if !ok {
		return
	}
	var body kapsorav1.CreateUpload
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewUploadInput{
		OriginalFilename: body.OriginalFilename, ContentType: body.ContentType,
		ByteSize: body.ByteSize, Classification: domain.ClassInternal,
		OwnerOrganizationID: body.OwnerOrganizationId,
	}
	if body.Classification != nil {
		in.Classification = string(*body.Classification)
	}
	if body.Sha256 != nil {
		in.SHA256 = *body.Sha256
	}

	reservation, err := h.svc.CreateUpload(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.DocumentUpload{Document: documentView(reservation.Document)}
	if reservation.Upload != nil {
		out.Upload = &kapsorav1.PresignedUpload{
			Url: reservation.Upload.URL, Method: kapsorav1.PresignedUploadMethod(reservation.Upload.Method),
			ExpiresAt: reservation.Upload.ExpiresAt.UTC(), Headers: reservation.Upload.Headers,
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// CompleteUpload implements completeUpload.
func (h *Handler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionUpload)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "documentId", application.ErrObjectNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CompleteUpload
	if !decodeJSON(w, r, &body) {
		return
	}
	doc, err := h.svc.CompleteUpload(r.Context(), rc, id, body.Sha256, body.ByteSize)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, documentView(doc))
}

// GetDocument implements getDocument.
func (h *Handler) GetDocument(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "documentId", application.ErrObjectNotFound)
	if !ok {
		return
	}
	doc, err := h.svc.GetDocument(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, documentView(doc))
}

// DownloadDocument implements downloadDocument. The body is optional: a download with no
// stated reason is still audited, with an empty one.
func (h *Handler) DownloadDocument(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "documentId", application.ErrObjectNotFound)
	if !ok {
		return
	}
	var body kapsorav1.DownloadDocument
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	req := application.DownloadRequest{}
	if body.PurposeCode != nil {
		req.PurposeCode = *body.PurposeCode
	}
	if body.ReasonText != nil {
		req.ReasonText = *body.ReasonText
	}

	url, doc, err := h.svc.Download(r.Context(), rc, id, req)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.DocumentDownload{
		Url: url.URL, Method: kapsorav1.DocumentDownloadMethod(url.Method),
		ExpiresAt:      url.ExpiresAt.UTC(),
		Classification: kapsorav1.DocumentClassification(doc.Object.Classification),
	})
}

// documentView renders one document with its links. The object key is deliberately absent:
// it is where the bytes are, and the only legitimate way to reach them is a presigned URL
// this API mints.
func documentView(doc application.Document) kapsorav1.Document {
	object := doc.Object
	out := kapsorav1.Document{
		Id:     object.ID,
		Bucket: kapsorav1.DocumentBucket(object.Bucket),

		Classification:   kapsorav1.DocumentClassification(object.Classification),
		OriginalFilename: object.OriginalFilename,
		ContentType:      object.ContentType,
		ByteSize:         object.ByteSize,
		ScanStatus:       kapsorav1.DocumentScanStatus(object.ScanStatus),
		Downloadable:     doc.Downloadable(),

		OwnerOrganizationId:   object.OwnerOrganizationID,
		DuplicateOfDocumentId: object.DuplicateOfObjectID,
		UploadedBy:            object.UploadedBy,
		UploadedAt:            object.UploadedAt.UTC(),
		PurgedAt:              utcPtr(object.PurgedAt),
		Links:                 make([]kapsorav1.DocumentLink, 0, len(doc.Links)),
		CreatedAt:             object.CreatedAt.UTC(),
		RowVersion:            object.RowVersion,
	}
	if len(object.SHA256) > 0 {
		digest := hex.EncodeToString(object.SHA256)
		out.Sha256 = &digest
	}
	for _, link := range doc.Links {
		out.Links = append(out.Links, linkView(link))
	}
	return out
}
