package notificationhttp

import (
	"net/http"
	"strconv"
	"strings"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// GetNotificationPreferences implements getNotificationPreferences.
func (h *Handler) GetNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	var fields []domain.FieldError
	recipientID := queryUUID(r, "recipientId", &fields)
	recipientType := strings.TrimSpace(r.URL.Query().Get("recipientType"))
	if recipientID == nil && len(fields) == 0 {
		fields = append(fields, domain.FieldError{
			Field: "recipientId", Code: "REQUIRED", Message: "alıcı kimliği gerekli",
		})
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	rows, err := h.svc.GetPreferences(r.Context(), rc,
		application.Recipient{Type: recipientType, ID: *recipientID})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preferenceList(rows))
}

// PutNotificationPreferences implements putNotificationPreferences.
func (h *Handler) PutNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireManage(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutNotificationPreferences
	if !decodeJSON(w, r, &body) {
		return
	}

	var fields []domain.FieldError
	in := make([]application.PreferenceInput, 0, len(body.Preferences))
	for i, item := range body.Preferences {
		input := application.PreferenceInput{
			EventCode: deref(item.EventCode), Channel: string(item.Channel),
			Enabled: item.Enabled, Timezone: deref(item.Timezone),
		}
		// The times are parsed here rather than in the service, because HH:MM is a shape
		// of the wire format: the service works in minutes since midnight, and a caller
		// who sent something else deserves a field error naming the field they sent.
		input.QuietHoursStart = parseClock(item.QuietHoursStart, i, "quietHoursStart", &fields)
		input.QuietHoursEnd = parseClock(item.QuietHoursEnd, i, "quietHoursEnd", &fields)
		in = append(in, input)
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	written, err := h.svc.PutPreferences(r.Context(), rc,
		application.Recipient{Type: string(body.RecipientType), ID: body.RecipientId}, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preferenceList(written))
}

// parseClock turns an optional HH:MM into a time of day, collecting the field error rather
// than answering one, so a body with two bad times names both.
func parseClock(raw *string, index int, field string, fields *[]domain.FieldError) *domain.ClockTime {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	value, err := domain.ParseClockTime(*raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field:   "preferences[" + strconv.Itoa(index) + "]." + field,
			Code:    "FORMAT",
			Message: "SS:DD biçiminde bir saat olmalı",
		})
		return nil
	}
	return &value
}

func preferenceList(rows []application.PreferenceRecord) kapsorav1.NotificationPreferenceList {
	out := kapsorav1.NotificationPreferenceList{
		Items: make([]kapsorav1.NotificationPreference, 0, len(rows)),
	}
	for _, row := range rows {
		out.Items = append(out.Items, preferenceView(row))
	}
	return out
}

func preferenceView(p application.PreferenceRecord) kapsorav1.NotificationPreference {
	out := kapsorav1.NotificationPreference{
		Id: p.ID, RecipientType: kapsorav1.NotificationRecipientType(p.RecipientType),
		RecipientId: p.RecipientID, EventCode: p.EventCode,
		Channel: kapsorav1.NotificationChannel(p.Channel), Enabled: p.Enabled,
		Timezone: p.Timezone, CreatedAt: p.CreatedAt.UTC(), RowVersion: p.RowVersion,
	}
	if p.QuietHoursStart != nil {
		start := p.QuietHoursStart.String()
		out.QuietHoursStart = &start
	}
	if p.QuietHoursEnd != nil {
		end := p.QuietHoursEnd.String()
		out.QuietHoursEnd = &end
	}
	return out
}
