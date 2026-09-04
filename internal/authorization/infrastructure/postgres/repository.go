// Package authorizationpg implements the authorization repository with sqlc. It is
// stateless: every method takes the caller's tenant-bound transaction, so RLS is active
// for every statement and nothing here can read another tenant's promise.
//
// The provider boundary lives here rather than above: every read takes the caller's scope
// and hands it to SQL, so a row outside it is genuinely not returned. That is what lets
// the application layer answer 404 without ever having held the row.
//
// Where a read already exists elsewhere it is reused rather than rewritten: the request
// header and its decided lines come from the service request module's own queries, the
// catalogue codes from its definition read, and the balances from the entitlement
// ledger's read side. There is no statement in this package that writes an account or a
// ledger row: entitlement moves only through benefit/ledger.
package authorizationpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// PostgreSQL error codes and the constraint names this package maps to named errors.
const (
	uniqueViolation = "23505"

	constraintReference      = "uq_authorization_reference"
	constraintIdempotency    = "uq_authorization_idempotency"
	constraintFulfilmentRef  = "uq_fulfilment_reference"
	constraintAuthorizedLine = "uq_authorization_item_request_item"
)

// Repository implements application.Repository.
type Repository struct {
	ledger *ledger.Ledger
}

// New returns the repository. The ledger is held for its read side only — ResolveAccounts
// lists what a person can spend from — and never for a movement: the application layer
// owns those and drives the engine itself.
func New() *Repository { return &Repository{ledger: ledger.NewLedger(nil)} }

var _ application.Repository = (*Repository)(nil)

// CreateAuthorization implements application.Repository.
func (Repository) CreateAuthorization(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewAuthorizationRow,
) (application.AuthorizationRecord, error) {
	row, err := sqlcgen.New(tx).CreateAuthorization(ctx, sqlcgen.CreateAuthorizationParams{
		TenantID: tenantID, RequestID: in.RequestID, AuthorizationReference: in.Reference,
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		ReservedTotal: in.ReservedTotal.String(), CurrencyCode: in.CurrencyCode,
		PriceQuoteID: optUUID(in.PriceQuoteID), ActorID: optUUID(in.ActorID),
		ApprovedAt: in.ApprovedAt, IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			switch pgErr.ConstraintName {
			case constraintReference:
				return application.AuthorizationRecord{}, application.ErrReferenceCollision
			case constraintIdempotency:
				return application.AuthorizationRecord{}, application.ErrKeyReplay
			}
		}
		return application.AuthorizationRecord{}, fmt.Errorf("authorization: create authorization: %w", err)
	}
	return application.AuthorizationRecord{
		ID: row.ID, RequestID: in.RequestID, Reference: row.AuthorizationReference,
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Status: row.Status,
		ReservedTotal: benefitdomain.TrimDecimal(in.ReservedTotal.String()), ConsumedTotal: "0",
		CurrencyCode: in.CurrencyCode, PriceQuoteID: in.PriceQuoteID,
		ApprovedBy: in.ActorID, ApprovedAt: in.ApprovedAt,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetAuthorization implements application.Repository.
func (Repository) GetAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.AuthorizationRecord, error) {
	row, err := sqlcgen.New(tx).GetAuthorization(ctx, sqlcgen.GetAuthorizationParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AuthorizationRecord{}, application.ErrAuthorizationNotFound
	}
	if err != nil {
		return application.AuthorizationRecord{}, fmt.Errorf("authorization: get authorization: %w", err)
	}
	return authorizationOf(authorizationRow(row)), nil
}

// LockAuthorization implements application.Repository.
func (Repository) LockAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.AuthorizationRecord, error) {
	row, err := sqlcgen.New(tx).LockAuthorization(ctx, sqlcgen.LockAuthorizationParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AuthorizationRecord{}, application.ErrAuthorizationNotFound
	}
	if err != nil {
		return application.AuthorizationRecord{}, fmt.Errorf("authorization: lock authorization: %w", err)
	}
	return authorizationOf(lockedAuthorizationRow(row)), nil
}

// GetAuthorizationByKey implements application.Repository.
func (Repository) GetAuthorizationByKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	key string,
) (application.AuthorizationRecord, error) {
	row, err := sqlcgen.New(tx).GetAuthorizationByIdempotencyKey(ctx,
		sqlcgen.GetAuthorizationByIdempotencyKeyParams{TenantID: tenantID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AuthorizationRecord{}, application.ErrAuthorizationNotFound
	}
	if err != nil {
		return application.AuthorizationRecord{}, fmt.Errorf("authorization: get authorization by key: %w", err)
	}
	return authorizationOf(keyedAuthorizationRow(row)), nil
}

// ListAuthorizations implements application.Repository.
func (Repository) ListAuthorizations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.AuthorizationQuery,
) ([]application.AuthorizationRecord, error) {
	params := sqlcgen.ListAuthorizationsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		Status: optionalString(q.Status), RequestID: optUUID(q.RequestID),
		PersonID: optUUID(q.PersonID), ProviderID: optUUID(q.ProviderOrganizationID),
		ValidFrom: q.ValidFrom, ValidTo: q.ValidTo, PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListAuthorizations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("authorization: list authorizations: %w", err)
	}
	out := make([]application.AuthorizationRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, authorizationOf(listedAuthorizationRow(row)))
	}
	return out, nil
}

// ExtendAuthorization implements application.Repository.
func (Repository) ExtendAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	validTo time.Time, actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).ExtendAuthorization(ctx, sqlcgen.ExtendAuthorizationParams{
		TenantID: tenantID, ID: id, ValidTo: validTo, ActorID: optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("authorization: extend authorization: %w", err)
	}
	if affected != 1 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CancelAuthorization implements application.Repository.
func (Repository) CancelAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	reasonCode string, actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).CancelAuthorization(ctx, sqlcgen.CancelAuthorizationParams{
		TenantID: tenantID, ID: id, CancelReasonCode: &reasonCode,
		ActorID: optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("authorization: cancel authorization: %w", err)
	}
	if affected != 1 {
		return application.ErrVersionMismatch
	}
	return nil
}

// ApplyConsumption implements application.Repository.
func (Repository) ApplyConsumption(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	delta benefitdomain.Quantity, actorID *uuid.UUID,
) error {
	affected, err := sqlcgen.New(tx).ApplyAuthorizationConsumption(ctx,
		sqlcgen.ApplyAuthorizationConsumptionParams{
			TenantID: tenantID, ID: id, DeltaConsumed: delta.String(), ActorID: optUUID(actorID),
		})
	if err != nil {
		return fmt.Errorf("authorization: apply consumption: %w", err)
	}
	if affected != 1 {
		return application.ErrAuthorizationNotActive
	}
	return nil
}

// ListExpirableAuthorizations implements application.Repository.
func (Repository) ListExpirableAuthorizations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	before time.Time, limit int,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListExpirableAuthorizations(ctx, sqlcgen.ListExpirableAuthorizationsParams{
		TenantID: tenantID, ExpiredBefore: before, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("authorization: list expirable authorizations: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out, nil
}

// MarkAuthorizationExpired implements application.Repository.
func (Repository) MarkAuthorizationExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkAuthorizationExpired(ctx, sqlcgen.MarkAuthorizationExpiredParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("authorization: mark authorization expired: %w", err)
	}
	return affected == 1, nil
}

// CreateAuthorizationItem implements application.Repository.
func (Repository) CreateAuthorizationItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewAuthorizationItemRow,
) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateAuthorizationItem(ctx, sqlcgen.CreateAuthorizationItemParams{
		TenantID: tenantID, AuthorizationID: in.AuthorizationID, RequestItemID: in.RequestItemID,
		ServiceDefinitionID: in.ServiceDefinitionID,
		ApprovedQuantity:    in.ApprovedQuantity.String(),
		ApprovedAmount:      quantityPtr(in.ApprovedAmount),
		MemberAmount:        in.MemberAmount.String(),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintAuthorizedLine {
			return uuid.Nil, application.ErrItemNotInAuthorization
		}
		return uuid.Nil, fmt.Errorf("authorization: create authorization item: %w", err)
	}
	return row.ID, nil
}

// SetAuthorizationItemReservation implements application.Repository.
func (Repository) SetAuthorizationItemReservation(ctx context.Context, tx pgx.Tx,
	tenantID, itemID, reservationID uuid.UUID,
) error {
	affected, err := sqlcgen.New(tx).SetAuthorizationItemReservation(ctx,
		sqlcgen.SetAuthorizationItemReservationParams{
			TenantID: tenantID, ID: itemID, EntitlementReservationID: uuid.NullUUID{UUID: reservationID, Valid: true},
		})
	if err != nil {
		return fmt.Errorf("authorization: set item reservation: %w", err)
	}
	if affected != 1 {
		return application.ErrItemNotInAuthorization
	}
	return nil
}

// ListAuthorizationItems implements application.Repository.
func (Repository) ListAuthorizationItems(ctx context.Context, tx pgx.Tx, tenantID, authorizationID uuid.UUID,
	forUpdate bool,
) ([]application.AuthorizationItemRecord, error) {
	q := sqlcgen.New(tx)
	if forUpdate {
		rows, err := q.LockAuthorizationItems(ctx, sqlcgen.LockAuthorizationItemsParams{
			TenantID: tenantID, AuthorizationID: authorizationID,
		})
		if err != nil {
			return nil, fmt.Errorf("authorization: lock authorization items: %w", err)
		}
		out := make([]application.AuthorizationItemRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, authorizationItemOf(lockedItemRow(row)))
		}
		return out, nil
	}
	rows, err := q.ListAuthorizationItems(ctx, sqlcgen.ListAuthorizationItemsParams{
		TenantID: tenantID, AuthorizationID: authorizationID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorization: list authorization items: %w", err)
	}
	out := make([]application.AuthorizationItemRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, authorizationItemOf(listedItemRow(row)))
	}
	return out, nil
}

// AddAuthorizationItemConsumption implements application.Repository.
func (Repository) AddAuthorizationItemConsumption(ctx context.Context, tx pgx.Tx, tenantID, itemID uuid.UUID,
	delta benefitdomain.Quantity,
) error {
	affected, err := sqlcgen.New(tx).AddAuthorizationItemConsumption(ctx,
		sqlcgen.AddAuthorizationItemConsumptionParams{
			TenantID: tenantID, ID: itemID, DeltaConsumed: delta.String(),
		})
	if err != nil {
		return fmt.Errorf("authorization: add item consumption: %w", err)
	}
	if affected != 1 {
		return application.ErrItemNotInAuthorization
	}
	return nil
}

// CreateFulfilment implements application.Repository.
func (Repository) CreateFulfilment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewFulfilmentRow,
) (application.FulfilmentRecord, error) {
	row, err := sqlcgen.New(tx).CreateFulfilment(ctx, sqlcgen.CreateFulfilmentParams{
		TenantID: tenantID, FulfilmentReference: in.Reference, AuthorizationID: in.AuthorizationID,
		ProviderProfileID: optUUID(in.ProviderProfileID), LocationID: optUUID(in.LocationID),
		PractitionerID: optUUID(in.PractitionerID), PerformedAt: in.PerformedAt,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintFulfilmentRef {
			return application.FulfilmentRecord{}, application.ErrReferenceCollision
		}
		return application.FulfilmentRecord{}, fmt.Errorf("authorization: create fulfilment: %w", err)
	}
	return application.FulfilmentRecord{
		ID: row.ID, Reference: row.FulfilmentReference, AuthorizationID: in.AuthorizationID,
		ProviderProfileID: in.ProviderProfileID, LocationID: in.LocationID,
		PractitionerID: in.PractitionerID, PerformedAt: in.PerformedAt, Status: row.Status,
		RecordedBy: in.ActorID, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetFulfilment implements application.Repository.
func (Repository) GetFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.FulfilmentRecord, error) {
	row, err := sqlcgen.New(tx).GetFulfilment(ctx, sqlcgen.GetFulfilmentParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.FulfilmentRecord{}, application.ErrFulfilmentNotFound
	}
	if err != nil {
		return application.FulfilmentRecord{}, fmt.Errorf("authorization: get fulfilment: %w", err)
	}
	return fulfilmentOf(fulfilmentRow(row)), nil
}

// LockFulfilment implements application.Repository.
func (Repository) LockFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.FulfilmentRecord, error) {
	row, err := sqlcgen.New(tx).LockFulfilment(ctx, sqlcgen.LockFulfilmentParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.FulfilmentRecord{}, application.ErrFulfilmentNotFound
	}
	if err != nil {
		return application.FulfilmentRecord{}, fmt.Errorf("authorization: lock fulfilment: %w", err)
	}
	return fulfilmentOf(lockedFulfilmentRow(row)), nil
}

// ListFulfilments implements application.Repository.
func (Repository) ListFulfilments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.FulfilmentQuery,
) ([]application.FulfilmentRecord, error) {
	params := sqlcgen.ListFulfilmentsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		Status: optionalString(q.Status), AuthorizationID: optUUID(q.AuthorizationID),
		ProviderProfileID: optUUID(q.ProviderProfileID),
		PerformedFrom:     q.PerformedFrom, PerformedTo: q.PerformedTo,
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListFulfilments(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("authorization: list fulfilments: %w", err)
	}
	out := make([]application.FulfilmentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, fulfilmentOf(listedFulfilmentRow(row)))
	}
	return out, nil
}

// CompleteFulfilment implements application.Repository.
func (Repository) CompleteFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	completedAt time.Time, expected int64,
) error {
	affected, err := sqlcgen.New(tx).CompleteFulfilment(ctx, sqlcgen.CompleteFulfilmentParams{
		TenantID: tenantID, ID: id, CompletedAt: &completedAt, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("authorization: complete fulfilment: %w", err)
	}
	if affected != 1 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CancelFulfilment implements application.Repository.
func (Repository) CancelFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	cancelledAt time.Time, reasonCode string, expected int64,
) error {
	affected, err := sqlcgen.New(tx).CancelFulfilmentRecord(ctx, sqlcgen.CancelFulfilmentRecordParams{
		TenantID: tenantID, ID: id, CancelledAt: &cancelledAt,
		CancelReasonCode: &reasonCode, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("authorization: cancel fulfilment: %w", err)
	}
	if affected != 1 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CreateFulfilmentItem implements application.Repository.
func (Repository) CreateFulfilmentItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewFulfilmentItemRow,
) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateFulfilmentItem(ctx, sqlcgen.CreateFulfilmentItemParams{
		TenantID: tenantID, FulfilmentID: in.FulfilmentID,
		AuthorizationItemID: in.AuthorizationItemID, ServiceDefinitionID: in.ServiceDefinitionID,
		ActualQuantity: in.ActualQuantity.String(), ActualAmount: quantityPtr(in.ActualAmount),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("authorization: create fulfilment item: %w", err)
	}
	return row.ID, nil
}

// ListFulfilmentItems implements application.Repository.
func (Repository) ListFulfilmentItems(ctx context.Context, tx pgx.Tx, tenantID, fulfilmentID uuid.UUID,
) ([]application.FulfilmentItemRecord, error) {
	rows, err := sqlcgen.New(tx).ListFulfilmentItems(ctx, sqlcgen.ListFulfilmentItemsParams{
		TenantID: tenantID, FulfilmentID: fulfilmentID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorization: list fulfilment items: %w", err)
	}
	out := make([]application.FulfilmentItemRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.FulfilmentItemRecord{
			ID: row.ID, FulfilmentID: row.FulfilmentID,
			AuthorizationItemID: row.AuthorizationItemID,
			ServiceDefinitionID: row.ServiceDefinitionID,
			ActualQuantity:      trimDecimal(row.ActualQuantity),
			ActualAmount:        trimDecimalPtr(row.ActualAmount),
		})
	}
	return out, nil
}

// CountLiveVouchers implements application.Repository.
func (Repository) CountLiveVouchers(ctx context.Context, tx pgx.Tx, tenantID,
	authorizationID uuid.UUID,
) (int64, error) {
	n, err := sqlcgen.New(tx).CountLiveVouchers(ctx, sqlcgen.CountLiveVouchersParams{
		TenantID: tenantID, AuthorizationID: authorizationID,
	})
	if err != nil {
		return 0, fmt.Errorf("authorization: count live vouchers: %w", err)
	}
	return n, nil
}

// CreateVoucher implements application.Repository.
func (Repository) CreateVoucher(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewVoucherRow,
) (application.VoucherRecord, error) {
	row, err := sqlcgen.New(tx).CreateVoucher(ctx, sqlcgen.CreateVoucherParams{
		TenantID: tenantID, AuthorizationID: in.AuthorizationID, TokenHash: in.TokenHash,
		TokenMasked: in.TokenMasked, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.VoucherRecord{}, fmt.Errorf("authorization: create voucher: %w", err)
	}
	return application.VoucherRecord{
		ID: row.ID, AuthorizationID: in.AuthorizationID, MaskedToken: in.TokenMasked,
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Status: row.Status,
		IssuedAt: row.IssuedAt, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// LockVoucherByTokenHash implements application.Repository.
func (Repository) LockVoucherByTokenHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	hash []byte, scope application.Scope,
) (application.VoucherRecord, error) {
	row, err := sqlcgen.New(tx).GetVoucherByTokenHash(ctx, sqlcgen.GetVoucherByTokenHashParams{
		TenantID: tenantID, TokenHash: hash, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VoucherRecord{}, application.ErrVoucherNotFound
	}
	if err != nil {
		return application.VoucherRecord{}, fmt.Errorf("authorization: lock voucher: %w", err)
	}
	return voucherOf(hashedVoucherRow(row)), nil
}

// ListVouchers implements application.Repository.
func (Repository) ListVouchers(ctx context.Context, tx pgx.Tx, tenantID, authorizationID uuid.UUID,
) ([]application.VoucherRecord, error) {
	rows, err := sqlcgen.New(tx).ListVouchers(ctx, sqlcgen.ListVouchersParams{
		TenantID: tenantID, AuthorizationID: authorizationID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorization: list vouchers: %w", err)
	}
	out := make([]application.VoucherRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, voucherOf(listedVoucherRow(row)))
	}
	return out, nil
}

// MarkVoucherRedeemed implements application.Repository.
func (Repository) MarkVoucherRedeemed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	redeemedAt time.Time, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).RedeemVoucher(ctx, sqlcgen.RedeemVoucherParams{
		TenantID: tenantID, ID: id, RedeemedAt: &redeemedAt, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("authorization: redeem voucher: %w", err)
	}
	return affected == 1, nil
}

// GetRequest implements application.Repository. It reads the service request module's own
// row, boundary and all, so an authorization can never be granted against a request its
// caller may not see.
func (Repository) GetRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.RequestRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceRequest(ctx, sqlcgen.GetServiceRequestParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RequestRecord{}, application.ErrRequestNotFound
	}
	if err != nil {
		return application.RequestRecord{}, fmt.Errorf("authorization: get service request: %w", err)
	}
	return application.RequestRecord{
		ID: row.ID, Reference: row.RequestReference, PersonID: row.PersonID,
		ProgramID: row.ProgramID, EnrollmentID: row.EnrollmentID,
		ServiceDate: dateValue(row.ServiceDate), Status: row.Status,
		CurrentVersionNo: int(row.CurrentVersionNo),
	}, nil
}

// ListDecidedItems implements application.Repository.
func (Repository) ListDecidedItems(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID,
	versionNo int,
) ([]application.RequestItemRecord, error) {
	q := sqlcgen.New(tx)
	version, err := q.GetServiceRequestVersionByNo(ctx, sqlcgen.GetServiceRequestVersionByNoParams{
		TenantID: tenantID, ServiceRequestID: requestID, VersionNo: int32(versionNo), //nolint:gosec // version numbers are small positive integers
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrRequestNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authorization: get request version: %w", err)
	}
	rows, err := q.ListServiceRequestItems(ctx, sqlcgen.ListServiceRequestItemsParams{
		TenantID: tenantID, ServiceRequestVersionID: version.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorization: list request items: %w", err)
	}
	out := make([]application.RequestItemRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.RequestItemRecord{
			ID: row.ID, LineNo: int(row.LineNo), ServiceDefinitionID: row.ServiceDefinitionID,
			UnitType: row.UnitType, Status: row.Status,
			ApprovedQuantity: trimDecimal(row.ApprovedQuantity),
			ApprovedAmount:   trimDecimal(row.ApprovedAmount),
			CurrencyCode:     row.CurrencyCode,
		})
	}
	return out, nil
}

// ServiceDefinitionCodes implements application.Repository.
func (Repository) ServiceDefinitionCodes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := sqlcgen.New(tx).ListServiceRequestServiceDefinitions(ctx,
		sqlcgen.ListServiceRequestServiceDefinitionsParams{TenantID: tenantID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("authorization: list service definitions: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = row.Code
	}
	return out, nil
}

// ResolveAccounts implements application.Repository.
func (r *Repository) ResolveAccounts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
	asOf time.Time,
) ([]ledger.Account, error) {
	return r.ledger.ResolveAccounts(ctx, tx, tenantID, personID, asOf)
}

// ActiveTenants implements application.Repository.
func (Repository) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
	if err != nil {
		return nil, fmt.Errorf("authorization: list tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("authorization: scan tenant: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authorization: list tenants: %w", err)
	}
	return out, nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func quantityPtr(q *benefitdomain.Quantity) *string {
	if q == nil {
		return nil
	}
	value := q.String()
	return &value
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return benefitdomain.DateOnly(d.Time)
}

// trimDecimal drops the trailing zeroes PostgreSQL prints for a numeric(20,6), so a
// quantity reads as "2" rather than "2.000000" wherever it is shown.
func trimDecimal(raw string) string { return benefitdomain.TrimDecimal(raw) }

// trimDecimalPtr turns the empty string the queries render a NULL numeric as back into the
// absence it stands for; anything else keeps its exact value with the trailing zeroes gone.
func trimDecimalPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	value := benefitdomain.TrimDecimal(raw)
	return &value
}

func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}
