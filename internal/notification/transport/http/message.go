package notificationhttp

import (
	"net/http"
	"strings"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// ListNotificationMessages implements listNotificationMessages.
func (h *Handler) ListNotificationMessages(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.MessageFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		EventCode:     strings.TrimSpace(r.URL.Query().Get("eventCode")),
		Channel:       strings.TrimSpace(r.URL.Query().Get("channel")),
		Status:        strings.TrimSpace(r.URL.Query().Get("status")),
		RecipientType: strings.TrimSpace(r.URL.Query().Get("recipientType")),
		RecipientID:   queryUUID(r, "recipientId", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListMessages(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.NotificationMessagePage{
		Items: make([]kapsorav1.NotificationMessage, 0, len(page.Items)),
	}
	for _, message := range page.Items {
		out.Items = append(out.Items, messageView(message))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// GetNotificationMessage implements getNotificationMessage.
func (h *Handler) GetNotificationMessage(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "messageId", application.ErrMessageNotFound)
	if !ok {
		return
	}
	detail, err := h.svc.GetMessage(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.NotificationMessageDetail{
		Message:    messageView(detail.Message),
		Deliveries: make([]kapsorav1.NotificationDelivery, 0, len(detail.Deliveries)),
	}
	for _, attempt := range detail.Deliveries {
		out.Deliveries = append(out.Deliveries, deliveryView(attempt))
	}
	writeJSON(w, http.StatusOK, out)
}

// ResendNotificationMessage implements resendNotificationMessage. It answers the copy it
// wrote rather than the original, because the copy is the message that is now on its way.
func (h *Handler) ResendNotificationMessage(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireManage(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "messageId", application.ErrMessageNotFound)
	if !ok {
		return
	}
	record, err := h.svc.ResendMessage(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, messageView(record))
}

func messageView(m application.MessageRecord) kapsorav1.NotificationMessage {
	out := kapsorav1.NotificationMessage{
		Id: m.ID, EventCode: m.EventCode,
		RecipientType: kapsorav1.NotificationRecipientType(m.RecipientType),
		RecipientId:   m.RecipientID,
		Channel:       kapsorav1.NotificationChannel(m.Channel), Locale: m.Locale,
		TemplateId: m.TemplateID, TemplateVersionNo: m.TemplateVersionNo,
		SubjectRendered: m.SubjectRendered, BodyRendered: m.BodyRendered,
		SafeVariables:       m.SafeVariables,
		Status:              kapsorav1.NotificationMessageStatus(m.Status),
		ResentFromMessageId: m.ResentFromMessageID,
		CreatedAt:           m.CreatedAt.UTC(), SentAt: m.SentAt, RowVersion: m.RowVersion,
	}
	if m.SafeVariables == nil {
		out.SafeVariables = map[string]string{}
	}
	if m.SuppressedReason != nil {
		reason := kapsorav1.NotificationSuppressionReason(*m.SuppressedReason)
		out.SuppressedReason = &reason
	}
	return out
}

func deliveryView(d application.DeliveryRecord) kapsorav1.NotificationDelivery {
	return kapsorav1.NotificationDelivery{
		Id: d.ID, MessageId: d.MessageID, AttemptNo: d.AttemptNo,
		ProviderCode: d.ProviderCode, ProviderMessageId: d.ProviderMessageID,
		Outcome: kapsorav1.NotificationDeliveryOutcome(d.Outcome), Detail: d.Detail,
		AttemptedAt: d.AttemptedAt.UTC(),
	}
}
