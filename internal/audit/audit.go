// Package audit defines the audit recording port every module writes through
// (v1.2 section 21). Audit rows are written inside the business transaction so a
// decision and its trace commit or roll back together. The PostgreSQL implementation
// is delivered by work package WP-I1-04; NopRecorder keeps other packages testable.
package audit

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Category mirrors audit.event.event_category.
type Category string

const (
	CategoryAccess         Category = "ACCESS"
	CategoryAuthentication Category = "AUTHENTICATION"
	CategoryBusiness       Category = "BUSINESS"
	CategoryAdmin          Category = "ADMIN"
	CategorySecurity       Category = "SECURITY"
	CategoryExport         Category = "EXPORT"
	CategoryPrivacy        Category = "PRIVACY"
)

// Outcome mirrors audit.event.outcome.
type Outcome string

const (
	OutcomeSuccess Outcome = "SUCCESS"
	OutcomeDenied  Outcome = "DENIED"
	OutcomeFailure Outcome = "FAILURE"
)

// Event is one audit.event row. Request id, trace id and source IP are taken from the
// context by the recorder; callers never pass them. Detail must contain only safe,
// non-personal values (ids, codes, counts): the recorder drops keys outside the
// allow-list it is configured with.
type Event struct {
	TenantID     uuid.NullUUID
	ActorID      uuid.NullUUID
	MembershipID uuid.NullUUID
	Category     Category
	ActionCode   string // e.g. "organization.create", "session.tenant_switch"
	ResourceType string
	ResourceID   uuid.NullUUID
	Outcome      Outcome
	ReasonCode   string
	PurposeCode  string
	Detail       map[string]any
	BeforeHash   []byte
	AfterHash    []byte
}

// AccessType mirrors audit.access_event.access_type.
type AccessType string

const (
	AccessView       AccessType = "VIEW"
	AccessSearch     AccessType = "SEARCH"
	AccessDownload   AccessType = "DOWNLOAD"
	AccessExport     AccessType = "EXPORT"
	AccessPrint      AccessType = "PRINT"
	AccessBreakGlass AccessType = "BREAK_GLASS"
)

// Classification mirrors audit.access_event.data_classification.
type Classification string

const (
	ClassInternal     Classification = "INTERNAL"
	ClassConfidential Classification = "CONFIDENTIAL"
	ClassPersonal     Classification = "PERSONAL"
	ClassHealth       Classification = "HEALTH"
)

// AccessEvent records who looked at whom (person, clinical, document, export).
type AccessEvent struct {
	TenantID       uuid.UUID
	ActorID        uuid.UUID
	MembershipID   uuid.NullUUID
	PersonID       uuid.NullUUID
	ResourceType   string
	ResourceID     uuid.NullUUID
	AccessType     AccessType
	Classification Classification
	PurposeCode    string
	ReasonText     string
	Outcome        Outcome
}

// Recorder writes audit rows within the caller's transaction.
type Recorder interface {
	Record(ctx context.Context, tx pgx.Tx, ev Event) error
	RecordAccess(ctx context.Context, tx pgx.Tx, ev AccessEvent) error
}

// NopRecorder discards events. Only for unit tests and for wiring before WP-I1-04 lands.
type NopRecorder struct{}

// Record implements Recorder.
func (NopRecorder) Record(context.Context, pgx.Tx, Event) error { return nil }

// RecordAccess implements Recorder.
func (NopRecorder) RecordAccess(context.Context, pgx.Tx, AccessEvent) error { return nil }
