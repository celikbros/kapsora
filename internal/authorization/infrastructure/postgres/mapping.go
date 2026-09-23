package authorizationpg

import (
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The three authorization reads select the same columns, and sqlc gives each of them its
// own row type; the item, fulfilment and voucher reads do the same. Rather than several
// copies of the same mapping — which is exactly how a column ends up carried in one read
// and dropped in another — every row is narrowed to one shape here and mapped once.

type authorization struct {
	ID                     uuid.UUID
	RequestID              uuid.UUID
	AuthorizationReference string
	ValidFrom              time.Time
	ValidTo                time.Time
	Status                 string
	ReservedTotal          string
	ConsumedTotal          string
	CurrencyCode           *string
	PriceQuoteID           uuid.NullUUID
	ApprovedBy             uuid.NullUUID
	ApprovedAt             time.Time
	CancelReasonCode       *string
	CreatedAt              time.Time
	RowVersion             int64
}

func authorizationRow(r sqlcgen.GetAuthorizationRow) authorization         { return authorization(r) }
func lockedAuthorizationRow(r sqlcgen.LockAuthorizationRow) authorization  { return authorization(r) }
func listedAuthorizationRow(r sqlcgen.ListAuthorizationsRow) authorization { return authorization(r) }
func keyedAuthorizationRow(r sqlcgen.GetAuthorizationByIdempotencyKeyRow) authorization {
	return authorization(r)
}

func authorizationOf(r authorization) application.AuthorizationRecord {
	return application.AuthorizationRecord{
		ID: r.ID, RequestID: r.RequestID, Reference: r.AuthorizationReference,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, Status: r.Status,
		ReservedTotal: trimDecimal(r.ReservedTotal), ConsumedTotal: trimDecimal(r.ConsumedTotal),
		CurrencyCode: r.CurrencyCode, PriceQuoteID: uuidPtr(r.PriceQuoteID),
		ApprovedBy: uuidPtr(r.ApprovedBy), ApprovedAt: r.ApprovedAt,
		CancelReasonCode: r.CancelReasonCode, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type authorizationItem struct {
	ID                       uuid.UUID
	AuthorizationID          uuid.UUID
	RequestItemID            uuid.UUID
	ServiceDefinitionID      uuid.UUID
	ApprovedQuantity         string
	ApprovedAmount           string
	MemberAmount             string
	EntitlementReservationID uuid.NullUUID
	ConsumedQuantity         string
	EntitlementUnitFactor    string
	RowVersion               int64
}

func listedItemRow(r sqlcgen.ListAuthorizationItemsRow) authorizationItem {
	return authorizationItem(r)
}

func lockedItemRow(r sqlcgen.LockAuthorizationItemsRow) authorizationItem {
	return authorizationItem(r)
}

func authorizationItemOf(r authorizationItem) application.AuthorizationItemRecord {
	return application.AuthorizationItemRecord{
		ID: r.ID, AuthorizationID: r.AuthorizationID, RequestItemID: r.RequestItemID,
		ServiceDefinitionID:   r.ServiceDefinitionID,
		ApprovedQuantity:      trimDecimal(r.ApprovedQuantity),
		ApprovedAmount:        trimDecimalPtr(r.ApprovedAmount),
		MemberAmount:          trimDecimal(r.MemberAmount),
		ReservationID:         uuidPtr(r.EntitlementReservationID),
		ConsumedQuantity:      trimDecimal(r.ConsumedQuantity),
		EntitlementUnitFactor: trimDecimal(r.EntitlementUnitFactor),
		RowVersion:            r.RowVersion,
	}
}

type fulfilment struct {
	ID                  uuid.UUID
	FulfilmentReference string
	AuthorizationID     uuid.UUID
	ProviderProfileID   uuid.NullUUID
	LocationID          uuid.NullUUID
	PractitionerID      uuid.NullUUID
	PerformedAt         time.Time
	Status              string
	CompletedAt         *time.Time
	CancelledAt         *time.Time
	CancelReasonCode    *string
	RecordedBy          uuid.NullUUID
	CreatedAt           time.Time
	RowVersion          int64
}

func fulfilmentRow(r sqlcgen.GetFulfilmentRow) fulfilment         { return fulfilment(r) }
func lockedFulfilmentRow(r sqlcgen.LockFulfilmentRow) fulfilment  { return fulfilment(r) }
func listedFulfilmentRow(r sqlcgen.ListFulfilmentsRow) fulfilment { return fulfilment(r) }

func fulfilmentOf(r fulfilment) application.FulfilmentRecord {
	return application.FulfilmentRecord{
		ID: r.ID, Reference: r.FulfilmentReference, AuthorizationID: r.AuthorizationID,
		ProviderProfileID: uuidPtr(r.ProviderProfileID), LocationID: uuidPtr(r.LocationID),
		PractitionerID: uuidPtr(r.PractitionerID), PerformedAt: r.PerformedAt, Status: r.Status,
		CompletedAt: r.CompletedAt, CancelledAt: r.CancelledAt,
		CancelReasonCode: r.CancelReasonCode, RecordedBy: uuidPtr(r.RecordedBy),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type voucher struct {
	ID                uuid.UUID
	AuthorizationID   uuid.UUID
	TokenMasked       string
	ValidFrom         time.Time
	ValidTo           time.Time
	Status            string
	RedeemedAt        *time.Time
	RedeemedByActorID uuid.NullUUID
	IssuedAt          time.Time
	CreatedAt         time.Time
	RowVersion        int64
}

func hashedVoucherRow(r sqlcgen.GetVoucherByTokenHashRow) voucher { return voucher(r) }
func listedVoucherRow(r sqlcgen.ListVouchersRow) voucher          { return voucher(r) }

func voucherOf(r voucher) application.VoucherRecord {
	return application.VoucherRecord{
		ID: r.ID, AuthorizationID: r.AuthorizationID, MaskedToken: r.TokenMasked,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, Status: r.Status,
		RedeemedAt: r.RedeemedAt, RedeemedByActorID: uuidPtr(r.RedeemedByActorID),
		IssuedAt: r.IssuedAt, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}
