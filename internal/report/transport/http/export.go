package reporthttp

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// ListExports answers GET /exports.
func (h *Handler) ListExports(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	// The caller's own by default. Widening it needs nothing more than `report.read`, which is
	// the same grant that already reads every figure the exports contain — what an export adds
	// is the file, and the file is reached through `downloadExport` and its own permission.
	page, err := h.svc.ListExports(r.Context(), rc, application.ExportFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		Kind: r.URL.Query().Get("kind"), Status: r.URL.Query().Get("status"),
		Mine: queryBool(r, "mine", true),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]kapsorav1.Export, 0, len(page.Items))
	for _, export := range page.Items {
		items = append(items, exportView(export))
	}
	writeJSON(w, http.StatusOK, kapsorav1.ExportPage{
		Items: items, NextCursor: nextCursor(page.NextCursor),
	})
}

// CreateExport answers POST /exports. It queues and returns 202: the worker writes the file, and a
// request that produced one would be a request whose duration is a function of how much data the
// tenant has.
func (h *Handler) CreateExport(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionExport)
	if !ok {
		return
	}
	var body kapsorav1.CreateExport
	if !decodeJSON(w, r, &body) {
		return
	}

	in := application.NewExportInput{
		Kind: string(body.Kind), Format: domain.FormatCSV,
		ProviderOrganizationID: body.ProviderOrganizationId,
		PeriodFrom:             dateValueOf(body.PeriodFrom), PeriodTo: dateValueOf(body.PeriodTo),
	}
	if body.Parameters != nil {
		in.Parameters = *body.Parameters
	}
	if body.Format != nil {
		in.Format = string(*body.Format)
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}

	export, err := h.svc.CreateExport(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, exportView(export))
}

// GetExport answers GET /exports/{exportId}.
func (h *Handler) GetExport(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "exportId", application.ErrExportNotFound)
	if !ok {
		return
	}
	export, err := h.svc.GetExport(r.Context(), rc, id, false)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, exportView(export))
}

// DownloadExport answers POST /exports/{exportId}/download.
//
// It is a POST rather than a GET because it changes state twice: the download counter moves and an
// access event is written. A GET that did either would be a GET a browser could repeat by going
// back a page, and the audit trail would count clicks nobody made.
//
// The permission is `report.export` rather than `report.read`: reading a figure on a screen and
// taking the file out of the building are different acts, and this is the second one.
func (h *Handler) DownloadExport(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionExport)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "exportId", application.ErrExportNotFound)
	if !ok {
		return
	}
	var body kapsorav1.ExportDownloadRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	var purpose, reason string
	if body.PurposeCode != nil {
		purpose = *body.PurposeCode
	}
	if body.ReasonText != nil {
		reason = *body.ReasonText
	}

	url, export, err := h.svc.DownloadExport(r.Context(), rc, id, purpose, reason, false)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.ExportDownload{
		Url: url.URL, Method: kapsorav1.ExportDownloadMethod(url.Method),
		ExpiresAt: url.ExpiresAt, Watermark: export.Watermark,
		DownloadCount: export.DownloadCount,
	})
}

func exportView(e application.Export) kapsorav1.Export {
	parameters := e.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	view := kapsorav1.Export{
		Id: e.ID, Kind: kapsorav1.ExportKind(e.Kind),
		Format: kapsorav1.ExportFormat(e.Format), Status: kapsorav1.ExportStatus(e.Status),
		Parameters:             parameters,
		ProviderOrganizationId: e.ProviderOrganizationID,
		PeriodFrom:             optDateOf(e.PeriodFrom), PeriodTo: optDateOf(e.PeriodTo),
		DocumentId: e.DocumentID, RowCount: e.RowCount,
		RequestedBy: e.RequestedBy, RequestedAt: e.RequestedAt, ExpiresAt: e.ExpiresAt,
		Watermark: e.Watermark, DownloadCount: e.DownloadCount, FailureCode: e.FailureCode,
		CreatedAt: e.CreatedAt, RowVersion: e.RowVersion,
	}
	if e.CurrencyCode != "" {
		currency := e.CurrencyCode
		view.CurrencyCode = &currency
	}
	return view
}

// dateValueOf reads a contract date into the plain time the service works in.
func dateValueOf(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	value := d.Time
	return &value
}
