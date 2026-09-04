package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the authorization, fulfilment and voucher use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	ledger  Ledger
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	logger  *slog.Logger
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Ledger is the movement engine of WP-I2-03. A nil one is built here, because there
	// is no configuration in which this package may run without it.
	Ledger Ledger
	Audit  audit.Recorder
	// Cursors may be nil in a process that only runs the expiry job: it never pages.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a window is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("authorization: pool and repository are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Ledger == nil {
		d.Ledger = ledger.NewLedger(d.Now)
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, ledger: d.Ledger, audit: d.Audit,
		cursors: d.Cursors, logger: d.Logger, now: d.Now,
	}, nil
}

// AuthorizationView is an authorization with its lines and the vouchers issued on it.
type AuthorizationView struct {
	Authorization AuthorizationRecord
	Items         []AuthorizationItemRecord
	Vouchers      []VoucherRecord
}

// AuthorizationPage is one keyset page of authorizations.
type AuthorizationPage struct {
	Items      []AuthorizationView
	NextCursor string
}

// FulfilmentView is a fulfilment with the lines it delivered.
type FulfilmentView struct {
	Fulfilment FulfilmentRecord
	Items      []FulfilmentItemRecord
}

// FulfilmentPage is one keyset page of fulfilments.
type FulfilmentPage struct {
	Items      []FulfilmentView
	NextCursor string
}

// IssuedVoucher is the one result that carries a plaintext token. It exists for exactly
// one response and is never read back from anywhere.
type IssuedVoucher struct {
	Voucher VoucherRecord
	Token   string
}

// AuthorizationFilter is the API-level authorization list request.
type AuthorizationFilter struct {
	Cursor                 string
	Limit                  int
	Status                 string
	RequestID              *uuid.UUID
	PersonID               *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	ValidFrom              *time.Time
	ValidTo                *time.Time
}

// FulfilmentFilter is the API-level fulfilment list request.
type FulfilmentFilter struct {
	Cursor            string
	Limit             int
	Status            string
	AuthorizationID   *uuid.UUID
	ProviderProfileID *uuid.UUID
	PerformedFrom     *time.Time
	PerformedTo       *time.Time
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// errPagingUnavailable guards the paged reads in a process built without a cursor codec:
// the scheduler runs the expiry job and never answers a list.
var errPagingUnavailable = errors.New("authorization: paged reads need a cursor codec")

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	if s.cursors == nil {
		return nil, 0, errPagingUnavailable
	}
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

// loadAuthorization reads one authorization with its lines and vouchers.
func (s *Service) loadAuthorization(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record AuthorizationRecord,
) (AuthorizationView, error) {
	items, err := s.repo.ListAuthorizationItems(ctx, tx, tenantID, record.ID, false)
	if err != nil {
		return AuthorizationView{}, err
	}
	vouchers, err := s.repo.ListVouchers(ctx, tx, tenantID, record.ID)
	if err != nil {
		return AuthorizationView{}, err
	}
	return AuthorizationView{Authorization: record, Items: items, Vouchers: vouchers}, nil
}

// reloadAuthorization re-reads an authorization after a write, so the caller is answered
// with the row that is actually in the database rather than with what the command
// believed it wrote.
func (s *Service) reloadAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope Scope,
) (AuthorizationView, error) {
	record, err := s.repo.GetAuthorization(ctx, tx, tenantID, id, scope)
	if err != nil {
		return AuthorizationView{}, err
	}
	return s.loadAuthorization(ctx, tx, tenantID, record)
}

func (s *Service) loadFulfilment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record FulfilmentRecord,
) (FulfilmentView, error) {
	items, err := s.repo.ListFulfilmentItems(ctx, tx, tenantID, record.ID)
	if err != nil {
		return FulfilmentView{}, err
	}
	return FulfilmentView{Fulfilment: record, Items: items}, nil
}

func (s *Service) reloadFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope Scope,
) (FulfilmentView, error) {
	record, err := s.repo.GetFulfilment(ctx, tx, tenantID, id, scope)
	if err != nil {
		return FulfilmentView{}, err
	}
	return s.loadFulfilment(ctx, tx, tenantID, record)
}

// record writes one business audit row. The detail is ids, codes, counts and exact
// decimal text only; a voucher's plaintext is never among them, which is why the issue
// command passes the masked tail and the voucher id instead.
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

// referenceEncoding is base32 without padding over uppercase letters and digits only,
// which is what somebody has to read out over a telephone.
var referenceEncoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// newReference builds a reference of the form <prefix>-20260904-XXXXXXXX. The random tail
// rather than a counter is deliberate: a per-tenant counter would leak how much work a
// sponsor is doing to anybody who can raise two authorizations and subtract.
func newReference(prefix string, now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("authorization: generate reference: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", prefix, now.UTC().Format("20060102"),
		referenceEncoding.EncodeToString(buf)), nil
}

// referenceAttempts is how many times a create retries a reference collision. The tail is
// forty random bits, so two attempts is already generous; the loop exists so that a
// collision is a retry rather than an error somebody has to read.
const referenceAttempts = 5

// remainderOf is the part of a line that is still held, as an exact quantity.
func remainderOf(item AuthorizationItemRecord) (benefitdomain.Quantity, error) {
	remaining, err := item.Remaining()
	if err != nil {
		return benefitdomain.Quantity{}, fmt.Errorf("authorization: line %s quantity: %w", item.ID, err)
	}
	return remaining, nil
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}

// authorizationCursor is the keyset position of a row on the (created_at DESC, id DESC)
// order both list endpoints page by.
func authorizationCursor(r AuthorizationRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

func fulfilmentCursor(r FulfilmentRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}
