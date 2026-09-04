package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewAuthorizationInput is the create command.
type NewAuthorizationInput struct {
	RequestID    uuid.UUID
	ValidFrom    *time.Time
	ValidTo      time.Time
	PriceQuoteID *uuid.UUID
	// MemberAmounts is the member's share per request line number.
	MemberAmounts map[int]string
	// IdempotencyKey is required: a create without one is a create that can reserve the
	// same balance twice.
	IdempotencyKey string
}

// ExtendInput is the extend command.
type ExtendInput struct {
	ValidTo         time.Time
	ReasonCode      string
	ReasonText      *string
	ExpectedVersion int64
}

// ReasonInput is the body of cancel: why, in a code somebody can count, and optionally in
// words somebody can read.
type ReasonInput struct {
	ReasonCode      string
	ReasonText      *string
	ExpectedVersion int64
}

// List returns a page of authorizations with their lines and vouchers.
func (s *Service) List(ctx context.Context, rc identity.RequestContext, f AuthorizationFilter) (AuthorizationPage, error) {
	if err := domain.ValidateStatusFilter(f.Status, domain.Statuses); err != nil {
		return AuthorizationPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return AuthorizationPage{}, err
	}

	var out AuthorizationPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListAuthorizations(ctx, tx, rc.TenantID, AuthorizationQuery{
			Scope: scopeOf(rc), Status: f.Status, RequestID: f.RequestID, PersonID: f.PersonID,
			ProviderOrganizationID: f.ProviderOrganizationID,
			ValidFrom:              f.ValidFrom, ValidTo: f.ValidTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(authorizationCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]AuthorizationView, 0, len(rows))
		for _, row := range rows {
			view, err := s.loadAuthorization(ctx, tx, rc.TenantID, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return AuthorizationPage{}, err
	}
	return out, nil
}

// Get returns one authorization with its lines and vouchers.
func (s *Service) Get(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (AuthorizationView, error) {
	var out AuthorizationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.reloadAuthorization(ctx, tx, rc.TenantID, id, scopeOf(rc))
		out = view
		return err
	})
	return out, err
}

// Create turns an approval into a hold. Everything happens in one transaction: the
// approved lines are read, entitlement is reserved for each of them through the ledger,
// and the reservation id is written onto the line. A refusal on any line rolls the whole
// thing back, because a half-reserved approval is a promise nobody can keep.
//
// The same key twice returns the first authorization and takes no second set of
// reservations. The check is made twice on purpose: once by reading the key, which
// answers the ordinary replay, and once by the unique index, which answers the two
// requests that arrived at the same moment and both found nothing.
func (s *Service) Create(ctx context.Context, rc identity.RequestContext, in NewAuthorizationInput) (AuthorizationView, error) {
	if in.IdempotencyKey == "" {
		return AuthorizationView{}, ErrIdempotencyKeyRequired
	}
	validFrom := s.now().UTC()
	if in.ValidFrom != nil {
		validFrom = in.ValidFrom.UTC()
	}
	if err := domain.ValidateNewAuthorization(domain.NewAuthorization{
		ValidFrom: validFrom, ValidTo: in.ValidTo, MemberAmounts: in.MemberAmounts,
	}); err != nil {
		return AuthorizationView{}, err
	}

	var out AuthorizationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.create(ctx, tx, rc, in, validFrom)
		out = view
		return err
	})
	// A key that was used a moment ago aborted the transaction above with a unique
	// violation, so the replay is answered from a fresh one.
	if errors.Is(err, ErrKeyReplay) {
		return s.replay(ctx, rc, in.IdempotencyKey)
	}
	return out, err
}

// create is the body of Create inside the caller's transaction.
func (s *Service) create(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewAuthorizationInput, validFrom time.Time,
) (AuthorizationView, error) {
	scope := scopeOf(rc)
	if existing, err := s.repo.GetAuthorizationByKey(ctx, tx, rc.TenantID, in.IdempotencyKey); err == nil {
		return s.loadAuthorization(ctx, tx, rc.TenantID, existing)
	} else if !errors.Is(err, ErrAuthorizationNotFound) {
		return AuthorizationView{}, err
	}

	request, err := s.repo.GetRequest(ctx, tx, rc.TenantID, in.RequestID, scope)
	if err != nil {
		return AuthorizationView{}, err
	}
	if !domain.Contains(domain.AuthorizableRequestStatuses, request.Status) {
		return AuthorizationView{}, ErrRequestNotApproved
	}
	lines, err := s.approvedLines(ctx, tx, rc.TenantID, request)
	if err != nil {
		return AuthorizationView{}, err
	}
	accounts, err := s.accountsFor(ctx, tx, rc.TenantID, request, lines)
	if err != nil {
		return AuthorizationView{}, err
	}

	reserved := benefitdomain.ZeroQuantity()
	for _, line := range lines {
		reserved = reserved.Add(line.quantity)
	}
	record, err := s.createWithReference(ctx, tx, rc, NewAuthorizationRow{
		RequestID: request.ID, ValidFrom: validFrom, ValidTo: in.ValidTo.UTC(),
		ReservedTotal: reserved, CurrencyCode: currencyOf(lines), PriceQuoteID: in.PriceQuoteID,
		ApprovedAt: s.now().UTC(), IdempotencyKey: in.IdempotencyKey,
		ActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return AuthorizationView{}, err
	}

	for _, line := range lines {
		itemID, err := s.repo.CreateAuthorizationItem(ctx, tx, rc.TenantID, NewAuthorizationItemRow{
			AuthorizationID: record.ID, RequestItemID: line.item.ID,
			ServiceDefinitionID: line.item.ServiceDefinitionID,
			ApprovedQuantity:    line.quantity, ApprovedAmount: line.amount,
			MemberAmount: memberAmountOf(in.MemberAmounts, line.item.LineNo),
		})
		if err != nil {
			return AuthorizationView{}, err
		}
		// The hold references the line rather than the authorization: two lines of one
		// authorization may draw on the same account, and the ledger's uniqueness on
		// (reference type, reference id, account) would fold them into a single hold.
		reservation, err := s.ledger.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: rc.TenantID, AccountID: accounts[line.item.ID], Quantity: line.quantity,
			ReferenceType: ledger.ReferenceAuthorization, ReferenceID: itemID,
			Key: reserveKey(itemID), ReasonCode: "AUTHORIZATION",
			ActorID: rc.Principal.ActorID,
		})
		if err != nil {
			return AuthorizationView{}, err
		}
		if err := s.repo.SetAuthorizationItemReservation(ctx, tx, rc.TenantID, itemID, reservation.ID); err != nil {
			return AuthorizationView{}, err
		}
	}

	if err := s.record(ctx, tx, rc, "authorization.create", "authorization", record.ID,
		map[string]any{
			"request_id": request.ID.String(), "reference": record.Reference,
			"item_count": len(lines), "reserved_total": reserved.String(),
		}); err != nil {
		return AuthorizationView{}, err
	}
	return s.reloadAuthorization(ctx, tx, rc.TenantID, record.ID, scope)
}

// replay answers a key that was already used, in its own transaction.
func (s *Service) replay(ctx context.Context, rc identity.RequestContext, key string) (AuthorizationView, error) {
	var out AuthorizationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetAuthorizationByKey(ctx, tx, rc.TenantID, key)
		if err != nil {
			return err
		}
		view, err := s.loadAuthorization(ctx, tx, rc.TenantID, record)
		out = view
		return err
	})
	return out, err
}

// createWithReference retries a reference collision rather than reporting it. The tail is
// random, so a collision means two authorizations drew the same forty bits on the same
// day, which is worth one more draw and no more explanation than that.
func (s *Service) createWithReference(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	row NewAuthorizationRow,
) (AuthorizationRecord, error) {
	for attempt := 0; attempt < referenceAttempts; attempt++ {
		reference, err := newReference("AUT", s.now())
		if err != nil {
			return AuthorizationRecord{}, err
		}
		row.Reference = reference
		record, err := s.repo.CreateAuthorization(ctx, tx, rc.TenantID, row)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrReferenceCollision):
			continue
		default:
			return AuthorizationRecord{}, err
		}
	}
	return AuthorizationRecord{}, ErrReferenceCollision
}

// approvedLine is one request line worth reserving, with its decision parsed.
type approvedLine struct {
	item     RequestItemRecord
	quantity benefitdomain.Quantity
	amount   *benefitdomain.Quantity
}

// approvedLines reads the decided lines of the request's current version and keeps the
// ones that carry something to promise. A rejected line and a line approved for nothing
// are the same thing here: there is no hold to take.
func (s *Service) approvedLines(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	request RequestRecord,
) ([]approvedLine, error) {
	items, err := s.repo.ListDecidedItems(ctx, tx, tenantID, request.ID, request.CurrentVersionNo)
	if err != nil {
		return nil, err
	}
	out := make([]approvedLine, 0, len(items))
	for _, item := range items {
		if !domain.Contains(domain.ApprovedItemStatuses, item.Status) {
			continue
		}
		quantity, err := benefitdomain.ParseQuantity(item.ApprovedQuantity)
		if err != nil || !quantity.IsPositive() {
			continue
		}
		line := approvedLine{item: item, quantity: quantity}
		if item.ApprovedAmount != "" {
			amount, err := benefitdomain.ParseQuantity(item.ApprovedAmount)
			if err != nil {
				return nil, fmt.Errorf("authorization: line %d amount: %w", item.LineNo, err)
			}
			line.amount = &amount
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, ErrNoApprovedItems
	}
	return out, nil
}

// accountsFor maps every line onto the entitlement account it will be held against. The
// convention is the one WP-I4-01's submit gate already uses: the service definition's own
// code names the entitlement, because there is no catalogue-to-entitlement table yet. A
// line that maps onto nothing fails the whole authorization rather than being promised
// against a balance nobody chose.
func (s *Service) accountsFor(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	request RequestRecord, lines []approvedLine,
) (map[uuid.UUID]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(lines))
	for _, line := range lines {
		ids = append(ids, line.item.ServiceDefinitionID)
	}
	codes, err := s.repo.ServiceDefinitionCodes(ctx, tx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	accounts, err := s.repo.ResolveAccounts(ctx, tx, tenantID, request.PersonID, request.ServiceDate)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]uuid.UUID, len(accounts))
	for _, account := range accounts {
		if _, seen := byCode[account.Definition.Code]; !seen {
			byCode[account.Definition.Code] = account.ID
		}
	}
	out := make(map[uuid.UUID]uuid.UUID, len(lines))
	for _, line := range lines {
		accountID, ok := byCode[codes[line.item.ServiceDefinitionID]]
		if !ok {
			return nil, ErrAccountNotFound
		}
		out[line.item.ID] = accountID
	}
	return out, nil
}

// Extend moves the end of a promise forward. It refuses anything but ACTIVE, and it
// refuses a date that is not later than the current one: shortening a promise somebody is
// relying on is a cancellation with a friendlier name.
func (s *Service) Extend(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ExtendInput,
) (AuthorizationView, error) {
	var out AuthorizationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if err := domain.ValidateExtension(current.ValidTo, in.ValidTo, in.ReasonCode, in.ReasonText); err != nil {
			return err
		}
		if current.Status != domain.StatusActive {
			return ErrAuthorizationNotActive
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		if err := s.repo.ExtendAuthorization(ctx, tx, rc.TenantID, id, in.ValidTo.UTC(),
			actorPtr(rc.Principal.ActorID), in.ExpectedVersion); err != nil {
			return err
		}
		detail := map[string]any{
			"from_valid_to": current.ValidTo.UTC().Format(time.RFC3339),
			"valid_to":      in.ValidTo.UTC().Format(time.RFC3339),
		}
		if in.ReasonCode != "" {
			detail["reason_code"] = in.ReasonCode
		}
		if err := s.record(ctx, tx, rc, "authorization.extend", "authorization", id, detail); err != nil {
			return err
		}
		out, err = s.reloadAuthorization(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	return out, err
}

// Cancel withdraws a promise and releases every hold that is still outstanding, in one
// transaction. What was already delivered stays consumed: cancelling a promise does not
// undo a service.
func (s *Service) Cancel(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (AuthorizationView, error) {
	if err := domain.ValidateReason(in.ReasonCode, in.ReasonText); err != nil {
		return AuthorizationView{}, err
	}

	var out AuthorizationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if !domain.Open(current.Status) {
			return ErrAuthorizationNotActive
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		released, err := s.releaseHolds(ctx, tx, rc.TenantID, rc.Principal.ActorID, id, "CANCELLED")
		if err != nil {
			return err
		}
		if err := s.repo.CancelAuthorization(ctx, tx, rc.TenantID, id, in.ReasonCode,
			actorPtr(rc.Principal.ActorID), in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "authorization.cancel", "authorization", id, map[string]any{
			"reason_code": in.ReasonCode, "released_total": released.String(),
		}); err != nil {
			return err
		}
		out, err = s.reloadAuthorization(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	return out, err
}

// releaseHolds gives every line's remainder back to the balance it came from and reports
// how much was released. The lines are taken FOR UPDATE first, so the remainder computed
// here is one nobody else can change before this transaction commits.
//
// A hold the ledger has already closed is skipped rather than treated as an error: the
// entitlement is back where it belongs either way, and refusing the cancellation would
// leave an authorization nobody can withdraw.
func (s *Service) releaseHolds(ctx context.Context, tx pgx.Tx, tenantID, actorID, authorizationID uuid.UUID,
	reason string,
) (benefitdomain.Quantity, error) {
	items, err := s.repo.ListAuthorizationItems(ctx, tx, tenantID, authorizationID, true)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	released := benefitdomain.ZeroQuantity()
	for _, item := range items {
		if item.ReservationID == nil {
			continue
		}
		remaining, err := remainderOf(item)
		if err != nil {
			return benefitdomain.Quantity{}, err
		}
		if !remaining.IsPositive() {
			continue
		}
		_, err = s.ledger.Release(ctx, tx, ledger.MovementInput{
			TenantID: tenantID, ReservationID: *item.ReservationID, Quantity: remaining,
			Key: releaseKey(item.ID, reason), ReasonCode: reason, ActorID: actorID,
		})
		switch {
		case errors.Is(err, ledger.ErrIdempotentReplay), errors.Is(err, ledger.ErrReservationClosed):
			continue
		case err != nil:
			return benefitdomain.Quantity{}, err
		}
		released = released.Add(remaining)
	}
	return released, nil
}

// reserveKey is the ledger idempotency key of a line's hold. It is derived from the line
// rather than from the request, so replaying a create can never take a second hold.
func reserveKey(itemID uuid.UUID) string { return "authorization-item:" + itemID.String() }

// releaseKey is the ledger idempotency key of a line's release. The reason is part of it
// so that a cancellation and an expiry of the same line are two distinct movements, and
// running either of them twice is still one.
func releaseKey(itemID uuid.UUID, reason string) string {
	return "authorization-release:" + reason + ":" + itemID.String()
}

// consumeKey is the ledger idempotency key of one delivered line: completing the same
// fulfilment twice consumes once.
func consumeKey(fulfilmentItemID uuid.UUID) string {
	return "fulfilment-item:" + fulfilmentItemID.String()
}

// currencyOf is the currency the authorized amounts are in, or nil when the lines carry
// no money at all — a session count has no currency and inventing one would be a lie.
func currencyOf(lines []approvedLine) *string {
	for _, line := range lines {
		if line.amount != nil && line.item.CurrencyCode != nil {
			return line.item.CurrencyCode
		}
	}
	return nil
}

func memberAmountOf(amounts map[int]string, lineNo int) benefitdomain.Quantity {
	raw, ok := amounts[lineNo]
	if !ok {
		return benefitdomain.ZeroQuantity()
	}
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return benefitdomain.ZeroQuantity()
	}
	return value
}
