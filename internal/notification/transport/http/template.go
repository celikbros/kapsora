package notificationhttp

import (
	"net/http"
	"strings"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/notification/application"
)

// ListNotificationTemplates implements listNotificationTemplates.
func (h *Handler) ListNotificationTemplates(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	page, err := h.svc.ListTemplates(r.Context(), rc, application.TemplateFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		EventCode: strings.TrimSpace(r.URL.Query().Get("eventCode")),
		Channel:   strings.TrimSpace(r.URL.Query().Get("channel")),
		Locale:    strings.TrimSpace(r.URL.Query().Get("locale")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.NotificationTemplatePage{
		Items: make([]kapsorav1.NotificationTemplate, 0, len(page.Items)),
	}
	for _, template := range page.Items {
		out.Items = append(out.Items, templateView(template))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateNotificationTemplate implements createNotificationTemplate.
func (h *Handler) CreateNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireManage(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateNotificationTemplate
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewTemplateInput{
		EventCode: body.EventCode, Channel: string(body.Channel), Locale: body.Locale,
		Subject: deref(body.Subject), Body: body.Body,
	}
	if body.DeclaredVariables != nil {
		in.DeclaredVariables = make([]string, 0, len(*body.DeclaredVariables))
		for _, name := range *body.DeclaredVariables {
			in.DeclaredVariables = append(in.DeclaredVariables, string(name))
		}
	}
	record, err := h.svc.CreateTemplate(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusCreated, templateView(record))
}

// GetNotificationTemplate implements getNotificationTemplate.
func (h *Handler) GetNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "templateId", application.ErrTemplateNotFound)
	if !ok {
		return
	}
	record, err := h.svc.GetTemplate(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, templateView(record))
}

// PublishNotificationTemplate implements publishNotificationTemplate.
func (h *Handler) PublishNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireManage(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "templateId", application.ErrTemplateNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	record, err := h.svc.PublishTemplate(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, templateView(record))
}

func templateView(t application.TemplateRecord) kapsorav1.NotificationTemplate {
	out := kapsorav1.NotificationTemplate{
		Id: t.ID, EventCode: t.EventCode,
		Channel: kapsorav1.NotificationChannel(t.Channel), Locale: t.Locale,
		VersionNo: t.VersionNo, Status: kapsorav1.NotificationTemplateStatus(t.Status),
		Subject: t.Subject, Body: t.Body,
		DeclaredVariables: make([]kapsorav1.NotificationSafeVariable, 0, len(t.DeclaredVariables)),
		PublishedAt:       t.PublishedAt, PublishedBy: t.PublishedBy,
		CreatedAt: t.CreatedAt.UTC(), RowVersion: t.RowVersion,
	}
	for _, name := range t.DeclaredVariables {
		out.DeclaredVariables = append(out.DeclaredVariables, kapsorav1.NotificationSafeVariable(name))
	}
	return out
}
