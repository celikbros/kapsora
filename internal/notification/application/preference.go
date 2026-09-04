package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// PreferenceInput is one row of a putNotificationPreferences command. EventCode empty
// means "every event on this channel", which is how somebody turns a channel off once
// instead of once per event.
type PreferenceInput struct {
	EventCode       string
	Channel         string
	Enabled         bool
	QuietHoursStart *domain.ClockTime
	QuietHoursEnd   *domain.ClockTime
	Timezone        string
}

// DefaultTimezone is what a preference row falls back to. Quiet hours are read in the
// recipient's own zone; this is the zone of the tenant this product was built for.
const DefaultTimezone = "Europe/Istanbul"

// GetPreferences reads everything one recipient has said about being told things.
func (s *Service) GetPreferences(ctx context.Context, rc identity.RequestContext,
	r Recipient,
) ([]PreferenceRecord, error) {
	if !domain.ValidRecipientType(r.Type) {
		return nil, fieldError("recipientType", "ENUM", "geçerli bir alıcı türü olmalı")
	}
	var rows []PreferenceRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListPreferences(ctx, tx, rc.TenantID, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// PutPreferences replaces the whole set for one recipient in one transaction. It is a
// replace rather than a merge because the set is read as a whole: a merge would leave
// behind a channel the person believed they had turned off, and they would keep being
// written to on it.
func (s *Service) PutPreferences(ctx context.Context, rc identity.RequestContext,
	r Recipient, in []PreferenceInput,
) ([]PreferenceRecord, error) {
	rows, err := validatePreferenceSet(r, in)
	if err != nil {
		return nil, err
	}

	var written []PreferenceRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		written, err = s.repo.ReplacePreferences(ctx, tx, rc.TenantID, r, rows,
			actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		disabled := 0
		quiet := 0
		for _, row := range written {
			if !row.Enabled {
				disabled++
			}
			if row.QuietHoursStart != nil {
				quiet++
			}
		}
		return s.record(ctx, tx, rc, "notification.preference.put", r.ID, map[string]any{
			"recipient_type": r.Type, "preference_count": len(written),
			"disabled_count": disabled, "quiet_hours_count": quiet,
		})
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// validatePreferenceSet checks every row and refuses a set that says two things about the
// same channel: the database would refuse the second write anyway, and answering 422 with
// the duplicate named is a better answer than a unique violation.
func validatePreferenceSet(r Recipient, in []PreferenceInput) ([]NewPreferenceRow, error) {
	if !domain.ValidRecipientType(r.Type) {
		return nil, fieldError("recipientType", "ENUM", "geçerli bir alıcı türü olmalı")
	}
	ve := &domain.ValidationError{}
	seen := map[string]bool{}
	rows := make([]NewPreferenceRow, 0, len(in))
	for i, item := range in {
		timezone := item.Timezone
		if timezone == "" {
			timezone = DefaultTimezone
		}
		if err := domain.ValidatePreference(r.Type, item.EventCode, item.Channel,
			timezone, item.QuietHoursStart, item.QuietHoursEnd); err != nil {
			var inner *domain.ValidationError
			if errors.As(err, &inner) {
				for _, f := range inner.Fields {
					ve.Add(fmt.Sprintf("preferences[%d].%s", i, f.Field), f.Code, f.Message)
				}
				continue
			}
			return nil, err
		}
		key := item.EventCode + "\x00" + item.Channel
		if seen[key] {
			ve.Add(fmt.Sprintf("preferences[%d].channel", i), "DUPLICATE",
				"aynı olay ve kanal için iki tercih verilemez")
			continue
		}
		seen[key] = true
		rows = append(rows, NewPreferenceRow{
			EventCode: optionalPtr(item.EventCode), Channel: item.Channel,
			Enabled: item.Enabled, QuietHoursStart: item.QuietHoursStart,
			QuietHoursEnd: item.QuietHoursEnd, Timezone: timezone,
		})
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return rows, nil
}

// suppressionFor answers why this message must not be sent now, or the empty string when
// nothing is in the way. Both answers are facts about the recipient rather than failures,
// which is why each of them writes a message instead of returning an error.
func (s *Service) suppressionFor(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	r Recipient, eventCode, channel string, now time.Time,
) (string, error) {
	pref, found, err := s.repo.ResolvePreference(ctx, tx, tenantID, r, eventCode, channel)
	if err != nil {
		return "", err
	}
	if !found {
		// Nobody said anything, which means send it. Silence is consent for a
		// transactional notification about somebody's own benefit; it is not consent for
		// marketing, and this module sends none.
		return "", nil
	}
	if !pref.Enabled {
		return domain.SuppressedPreferenceDisabled, nil
	}
	if pref.QuietHoursStart == nil || pref.QuietHoursEnd == nil {
		return "", nil
	}
	quiet, err := domain.InQuietHours(now, pref.Timezone, *pref.QuietHoursStart, *pref.QuietHoursEnd)
	if err != nil {
		// An unresolvable zone is not "send it anyway". A message in the middle of
		// somebody's night because a zone name was misspelt is exactly what quiet hours
		// exist to prevent, so the send is retried rather than performed.
		return "", err
	}
	if quiet {
		return domain.SuppressedQuietHours, nil
	}
	return "", nil
}
