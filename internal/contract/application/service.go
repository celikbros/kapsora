package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the contract use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	// Now defaults to time.Now; tests pin it so a price resolution is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("contract: pool, repository and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors, now: d.Now}, nil
}

// ListFilter is the API-level list request of the paged readers.
type ListFilter struct {
	Query               string
	Cursor              string
	Limit               int
	ProviderProfileID   string
	PayerOrganizationID string
	DomainCode          string
	Status              string
}

// ContractPage is one page of contracts.
type ContractPage struct {
	Items      []ContractRecord
	NextCursor string
}

// PriceItemPage is one page of price items with the ETag of the owning list.
type PriceItemPage struct {
	Items      []PriceItemRecord
	ListID     uuid.UUID
	RowVersion int64
	NextCursor string
}

// VersionView is a contract version together with the price lists under it; the price
// items of each list are paged separately, because a health tariff runs to thousands.
type VersionView struct {
	Version    VersionRecord
	PriceLists []PriceListRecord
}

// PackageResult and QuotaResult are child sets with the ETag of their version, which is
// what a set replacement expects in If-Match.
type PackageResult struct {
	Items      []PackageRecord
	RowVersion int64
}

// QuotaResult is PackageResult for provider quotas.
type QuotaResult struct {
	Items      []QuotaRecord
	RowVersion int64
}

// PriceListResult is the price list set with the ETag of its version.
type PriceListResult struct {
	Items      []PriceListRecord
	RowVersion int64
}

// PaymentTermResult is the payment term with the ETag of its version, so that writing it
// and writing any other part of the sheet share one concurrency token.
type PaymentTermResult struct {
	Term       PaymentTermRecord
	RowVersion int64
}

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	decoded, hasCursor, err := s.cursors.Decode(cursor)
	if err != nil {
		return nil, 0, err
	}
	pageSize = httpx.ClampLimit(limit)
	if hasCursor {
		after = &decoded
	}
	return after, pageSize, nil
}

// nextCursor encodes the position of the last item kept on the page.
func (s *Service) nextCursor(createdAt time.Time, id uuid.UUID) string {
	return s.cursors.Encode(httpx.Cursor{CreatedAt: createdAt, ID: id})
}

// record writes one business audit row. Contract rows carry no personal data, so the
// detail names ids, codes and counts only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// recordDenied writes the refusal of a command in its own right. A denial that leaves no
// trace is worse than a noisy error.
func (s *Service) recordDenied(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, reason string, detail map[string]any,
) error {
	event := audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeDenied, Detail: detail,
	}
	event.ReasonCode = reason
	return s.audit.Record(ctx, tx, event)
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

// statusError distinguishes "the row is frozen" from "the command does not apply here".
// A published or retired version answers CONTRACT_VERSION_IMMUTABLE, which is the point of
// the whole package: what a claim was priced from cannot move.
func statusError(current string) error {
	if current == domain.VersionPublished || current == domain.VersionRetired {
		return ErrVersionImmutable
	}
	return ErrVersionTransition
}

// optString turns an empty string into the NULL the column stores.
func optString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// deref reads through an optional string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseOptionalUUID reads an optional identifier from a request field.
func parseOptionalUUID(raw, field string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add(field, "FORMAT", "geçerli bir kimlik olmalı")
		return nil, ve
	}
	return &id, nil
}

// appendFields folds a nested validation error into the collector.
func appendFields(ve *domain.ValidationError, err error) {
	var inner *domain.ValidationError
	if errors.As(err, &inner) {
		ve.Fields = append(ve.Fields, inner.Fields...)
	}
}

// changed joins the patched field names for the audit detail; the values themselves are
// never logged.
func changed(fields []string) string { return strings.Join(fields, ",") }

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}

// itemPath names one element of a request array in a validation error.
func itemPath(index int) string { return "items[" + strconv.Itoa(index) + "]" }

// itoa keeps the array index formatting of the nested paths in one place.
func itoa(n int) string { return strconv.Itoa(n) }

// httpxDefaultPage is the page the set writers answer with; a replacement returns the head
// of the new set rather than all of it, because a tariff can run to thousands of rows.
const httpxDefaultPage = httpx.DefaultPageSize
