// Package auditpg writes audit rows through sqlc inside the caller's transaction.
package auditpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Recorder implements audit.Recorder on audit.event and audit.access_event.
type Recorder struct{}

// New returns a recorder; it holds no state, the transaction comes from the caller.
func New() *Recorder { return &Recorder{} }

var _ audit.Recorder = (*Recorder)(nil)

// Record inserts one audit.event row. The detail map is sanitised, never rejected; a
// database failure aborts the caller's transaction because audit is mandatory.
func (Recorder) Record(ctx context.Context, tx pgx.Tx, ev audit.Event) error {
	if ev.Category == "" || ev.ActionCode == "" || ev.Outcome == "" {
		return errors.New("audit: category, action code and outcome are required")
	}
	detail, err := json.Marshal(audit.SanitizeDetail(ev.Detail))
	if err != nil {
		return fmt.Errorf("audit: encode detail: %w", err)
	}
	meta := audit.RequestMetaFrom(ctx)

	_, err = sqlcgen.New(tx).InsertAuditEvent(ctx, sqlcgen.InsertAuditEventParams{
		TenantID:      ev.TenantID,
		ActorID:       ev.ActorID,
		MembershipID:  ev.MembershipID,
		EventCategory: string(ev.Category),
		ActionCode:    ev.ActionCode,
		ResourceType:  optString(ev.ResourceType),
		ResourceID:    ev.ResourceID,
		Outcome:       string(ev.Outcome),
		RequestID:     meta.RequestID,
		TraceID:       optString(meta.TraceID),
		SourceIp:      optAddr(meta.SourceIP),
		UserAgentHash: meta.UserAgentHash,
		ReasonCode:    optString(ev.ReasonCode),
		PurposeCode:   optString(ev.PurposeCode),
		DetailJson:    detail,
		BeforeHash:    ev.BeforeHash,
		AfterHash:     ev.AfterHash,
	})
	if err != nil {
		return fmt.Errorf("audit: insert event %s: %w", ev.ActionCode, err)
	}
	return nil
}

// RecordAccess inserts one audit.access_event row.
func (Recorder) RecordAccess(ctx context.Context, tx pgx.Tx, ev audit.AccessEvent) error {
	if ev.ResourceType == "" || ev.AccessType == "" || ev.Classification == "" || ev.Outcome == "" {
		return errors.New("audit: resource type, access type, classification and outcome are required")
	}
	meta := audit.RequestMetaFrom(ctx)

	_, err := sqlcgen.New(tx).InsertAccessEvent(ctx, sqlcgen.InsertAccessEventParams{
		TenantID:           ev.TenantID,
		ActorID:            ev.ActorID,
		MembershipID:       ev.MembershipID,
		PersonID:           ev.PersonID,
		ResourceType:       ev.ResourceType,
		ResourceID:         ev.ResourceID,
		AccessType:         string(ev.AccessType),
		DataClassification: string(ev.Classification),
		PurposeCode:        optString(ev.PurposeCode),
		ReasonText:         optString(ev.ReasonText),
		Outcome:            string(ev.Outcome),
		RequestID:          meta.RequestID,
		TraceID:            optString(meta.TraceID),
		SourceIp:           optAddr(meta.SourceIP),
	})
	if err != nil {
		return fmt.Errorf("audit: insert access event %s: %w", ev.ResourceType, err)
	}
	return nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optAddr(a netip.Addr) *netip.Addr {
	if !a.IsValid() {
		return nil
	}
	return &a
}
