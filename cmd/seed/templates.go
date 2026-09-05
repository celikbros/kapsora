package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// ensureTemplates publishes every seed template that is not already published for the
// tenant. It is idempotent in the only way that matters here: a slot that already has a
// published template is left alone rather than republished, because publishing retires
// the standing one and a seed run should not renumber the tenant's template history.
func (s *seeder) ensureTemplates(ctx context.Context, tenantID uuid.UUID) error {
	rc := identity.RequestContext{TenantID: tenantID}
	published, skipped := 0, 0
	for _, t := range notificationapp.SeedTemplates() {
		existing, err := s.notifications.ListTemplates(ctx, rc, notificationapp.TemplateFilter{
			EventCode: t.EventCode, Channel: t.Channel,
			Locale: notificationapp.DefaultLocale, Status: domain.TemplatePublished, Limit: 1,
		})
		if err != nil {
			return fmt.Errorf("list templates for %s/%s: %w", t.EventCode, t.Channel, err)
		}
		if len(existing.Items) > 0 {
			skipped++
			continue
		}
		draft, err := s.notifications.CreateTemplate(ctx, rc, notificationapp.NewTemplateInput{
			EventCode: t.EventCode, Channel: t.Channel, Locale: notificationapp.DefaultLocale,
			Subject: t.Subject, Body: t.Body, DeclaredVariables: t.Variables,
		})
		if err != nil {
			return fmt.Errorf("create template %s/%s: %w", t.EventCode, t.Channel, err)
		}
		if _, err := s.notifications.PublishTemplate(ctx, rc, draft.ID, draft.RowVersion); err != nil {
			if errors.Is(err, notificationapp.ErrTemplateNotDraft) {
				continue
			}
			return fmt.Errorf("publish template %s/%s: %w", t.EventCode, t.Channel, err)
		}
		published++
	}
	fmt.Printf("notify  %-22s %d published, %d already there\n", "templates", published, skipped)
	return nil
}
