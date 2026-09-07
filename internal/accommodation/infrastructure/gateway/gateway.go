// Package accommodationgw is where a booking meets the three modules it cannot do without:
// the reservation request it is asked for with (WP-I4-01), the authorization that approval
// produces (WP-I4-02), and the contract terms it freezes (WP-I6-04).
//
// It exists so that none of those modules knows a booking exists and the accommodation
// module holds no copy of what they do. The ports are declared in accommodation/application
// in this package's own vocabulary -- nights, a room, a policy -- and the three adapters
// below are the only code in the repository that speaks both languages.
//
// Nothing here decides anything. Every refusal comes from the module being called: the
// eligibility gate is WP-I4-01's, the no-double-spend rule is the ledger's, and whether a
// version has lodging terms at all is WP-I6-04's to answer. An adapter that added a rule
// would be a rule nobody reading either module could find.
package accommodationgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/identity"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// reservationType is the request a booking is. It is spelled here rather than passed in: a
// stay in a hotel is a reservation, always, and a caller that could choose the type could
// book a room as a reimbursement.
const reservationType = "RESERVATION"

// Requests turns "ask for these nights" into WP-I4-01's create and submit, in the caller's
// transaction. Both halves happen here because a booking is asked for and handed over in
// one act: a confirmation whose request sat in DRAFT would be a room held for a request no
// reviewer would ever see.
type Requests struct{ svc *servicerequestapp.Service }

// NewRequests wraps the service request module.
func NewRequests(svc *servicerequestapp.Service) *Requests { return &Requests{svc: svc} }

var _ accommodationapp.RequestPort = (*Requests)(nil)

// CreateReservation implements accommodationapp.RequestPort.
//
// One line for the whole stay, with the night count as its quantity, rather than one line
// per night. A reviewer decides "four of the five nights" by reducing a quantity, which is
// the decision WP-I4-01 already knows how to record; five separate lines would make the
// same decision five approvals and would put a fortnight's stay in front of somebody as a
// fourteen-line request.
//
// The amount on the line is the quote's total, frozen at the hold. It is not recomputed
// here and it is not recomputed anywhere else: what the member saw is what the request asks
// for and what the booking's nights carry.
func (r *Requests) CreateReservation(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in accommodationapp.BookingRequestInput,
) (accommodationapp.BookingRequestRef, error) {
	provider := in.ProviderOrganizationID
	start, end := in.RequestedStartAt, in.RequestedEndAt
	item := servicerequestdomain.ItemInput{
		ServiceDefinitionID: in.ServiceDefinitionID.String(),
		RequestedQuantity:   strconv.Itoa(in.Nights),
		UnitType:            in.UnitType,
	}
	if in.Amount != "" && in.CurrencyCode != "" {
		item.RequestedAmount = in.Amount
		item.CurrencyCode = in.CurrencyCode
	}
	draft, err := r.svc.CreateInTx(ctx, tx, rc, servicerequestapp.NewRequestInput{
		RequestType: reservationType, PersonID: in.PersonID, EnrollmentID: in.EnrollmentID,
		ProviderOrganizationID: &provider, ServiceDate: in.ServiceDate,
		RequestedStartAt: &start, RequestedEndAt: &end, Channel: in.Channel,
		Items: []servicerequestdomain.ItemInput{item},
	})
	if err != nil {
		return accommodationapp.BookingRequestRef{}, err
	}
	// Straight through the gate. Whatever it decides -- approved outright, waiting for a
	// document, refused on eligibility -- is what the booking's request page will show, and
	// this package neither reads that decision nor acts on it: the outbox does.
	//
	// The one thing declared to the gate is what the hold already reserved, so the check does
	// not count this booking's own nights against it. Everything else -- the mapping, the
	// plan version, the rules, the document requirement -- runs exactly as it does for any
	// other request.
	held, err := heldQuantities(in)
	if err != nil {
		return accommodationapp.BookingRequestRef{}, err
	}
	submitted, err := r.svc.SubmitInTxHolding(ctx, tx, rc, draft.Request.ID, nil,
		draft.Request.RowVersion, held)
	if err != nil {
		return accommodationapp.BookingRequestRef{}, err
	}
	return accommodationapp.BookingRequestRef{
		ID: submitted.Request.ID, Reference: submitted.Request.Reference,
		Status: submitted.Request.Status,
	}, nil
}

// heldQuantities renders what the booking already reserved in the shape WP-I4-01's gate
// takes: the entitlement, in its own unit, keyed by the service the line names.
func heldQuantities(in accommodationapp.BookingRequestInput) (map[uuid.UUID]benefitdomain.Quantity, error) {
	if in.HeldNights == "" {
		return nil, nil
	}
	quantity, err := benefitdomain.ParseQuantity(in.HeldNights)
	if err != nil {
		return nil, fmt.Errorf("accommodation: held nights %q: %w", in.HeldNights, err)
	}
	if !quantity.IsPositive() {
		return nil, nil
	}
	return map[uuid.UUID]benefitdomain.Quantity{in.ServiceDefinitionID: quantity}, nil
}

// Authorizations is WP-I4-02 seen from the accommodation module: take a hold for what the
// reservation promised, and mint the voucher the member shows at the desk.
type Authorizations struct{ svc *authorizationapp.Service }

// NewAuthorizations wraps the authorization module.
func NewAuthorizations(svc *authorizationapp.Service) *Authorizations {
	return &Authorizations{svc: svc}
}

var _ accommodationapp.AuthorizationPort = (*Authorizations)(nil)

// CreateForRequest implements accommodationapp.AuthorizationPort.
//
// The one thing this adapter passes through that no other caller of WP-I4-02 does is the
// adoption. A booking has already reserved its nights -- at the hold, fifteen minutes
// before anybody approved anything -- so the authorization takes over that reservation
// instead of taking a second one. Without it the member's plan would be drawn down twice
// for one stay, both holds would be real, the ledger's own conservation would still be
// satisfied, and nothing anywhere would say so.
func (a *Authorizations) CreateForRequest(ctx context.Context, rc identity.RequestContext,
	in accommodationapp.BookingAuthorizationInput,
) (accommodationapp.BookingAuthorizationRef, error) {
	validFrom := in.ValidFrom
	input := authorizationapp.NewAuthorizationInput{
		RequestID: in.RequestID, ValidFrom: &validFrom, ValidTo: in.ValidTo,
		IdempotencyKey:            in.IdempotencyKey,
		AdoptReservationID:        in.AdoptReservationID,
		AdoptReservationExpiresAt: in.AdoptReservationExpiresAt,
	}
	if in.MemberAmount != "" {
		// Line one, because the booking raises exactly one line. The member's share is
		// carried onto the authorization so a later claim knows what the member already
		// owed before anything was delivered.
		input.MemberAmounts = map[int]string{1: in.MemberAmount}
	}
	view, err := a.svc.Create(ctx, rc, input)
	if err != nil {
		return accommodationapp.BookingAuthorizationRef{}, err
	}
	approved := benefitdomain.ZeroQuantity()
	for _, item := range view.Items {
		quantity, err := benefitdomain.ParseQuantity(item.ApprovedQuantity)
		if err != nil {
			return accommodationapp.BookingAuthorizationRef{},
				fmt.Errorf("accommodation: authorization line %s quantity: %w", item.ID, err)
		}
		approved = approved.Add(quantity)
	}
	return accommodationapp.BookingAuthorizationRef{
		ID: view.Authorization.ID, ApprovedNights: approved.String(),
		ValidTo: view.Authorization.ValidTo,
	}, nil
}

// IssueVoucher implements accommodationapp.AuthorizationPort, in the caller's transaction
// so the voucher and the `voucher_id` on the booking commit together.
//
// The plaintext token comes back in the result and goes nowhere else. What WP-I4-02 stores
// is a SHA-256 digest and a masked tail; this adapter neither logs the token, records it,
// nor puts it anywhere a caller could accidentally persist it.
func (a *Authorizations) IssueVoucher(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in accommodationapp.BookingVoucherInput,
) (accommodationapp.IssuedBookingVoucher, error) {
	validFrom, validTo := in.ValidFrom, in.ValidTo
	issued, err := a.svc.IssueVoucherInTx(ctx, tx, rc, authorizationapp.IssueVoucherInput{
		AuthorizationID: in.AuthorizationID, ValidFrom: &validFrom, ValidTo: &validTo,
		Replace: in.Replace, RevokeReasonCode: in.RevokeReasonCode,
	})
	if err != nil {
		return accommodationapp.IssuedBookingVoucher{}, err
	}
	return accommodationapp.IssuedBookingVoucher{
		ID: issued.Voucher.ID, MaskedToken: issued.Voucher.MaskedToken,
		ValidFrom: issued.Voucher.ValidFrom, ValidTo: issued.Voucher.ValidTo,
		Token: issued.Token,
	}, nil
}

// Policies is WP-I6-04's SnapshotLodgingPolicy seen from here.
type Policies struct{ svc *contractapp.Service }

// NewPolicies wraps the contract module.
func NewPolicies(svc *contractapp.Service) *Policies { return &Policies{svc: svc} }

var _ accommodationapp.LodgingPolicyPort = (*Policies)(nil)

// SnapshotPolicy implements accommodationapp.LodgingPolicyPort.
//
// The snapshot is stored as the contract's own wire shape rather than as a private struct
// of this vertical. That is deliberate: `booking.policy_snapshot` is read back by a screen
// and by WP-I6-03's cancellation arithmetic, and a second definition of the same document
// would be a second thing to keep in step with the schema. Marshalling the generated type
// means the JSON in the column is, by construction, the JSON the contract says it is.
//
// A version with no terms is ErrLodgingTermsMissing and not an empty policy. Inventing a
// cancellation policy here would be inventing a fee, and the member is told the stay cannot
// be agreed instead.
func (p *Policies) SnapshotPolicy(ctx context.Context, rc identity.RequestContext,
	contractVersionID uuid.UUID, timeZone string,
) (json.RawMessage, error) {
	snapshot, err := p.svc.SnapshotLodgingPolicy(ctx, rc, contractVersionID, timeZone)
	if errors.Is(err, contractapp.ErrLodgingTermsNotFound) {
		return nil, accommodationapp.ErrLodgingTermsMissing
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(policyView(snapshot))
	if err != nil {
		return nil, fmt.Errorf("accommodation: freeze lodging policy: %w", err)
	}
	return raw, nil
}

// policyView renders the snapshot in the contract's wire shape.
func policyView(s contractapp.LodgingPolicySnapshot) kapsorav1.LodgingPolicySnapshot {
	return kapsorav1.LodgingPolicySnapshot{
		ContractVersionId:           s.ContractVersionID,
		SnapshotAt:                  s.SnapshotAt.UTC(),
		Timezone:                    s.TimeZone,
		FreeCancellationHoursBefore: s.Terms.FreeCancellationHoursBefore,
		PenaltyKind:                 kapsorav1.LodgingPenaltyKind(s.Terms.PenaltyKind),
		PenaltyNights:               s.Terms.PenaltyNights,
		PenaltyPercent:              percentOrNil(s.Terms.PenaltyPercent),
		NoShowPercent:               s.Terms.NoShowPercent,
		HoldMinutes:                 s.Terms.HoldMinutes,
		MinNights:                   s.Terms.MinNights,
		MaxNights:                   s.Terms.MaxNights,
		ChildFreeUnderAge:           s.Terms.ChildFreeUnderAge,
	}
}

// percentOrNil keeps an optional percentage a string the whole way. The generated type is
// an alias for string and the record holds the canonical decimal the column does; nothing
// between the two parses it into a number.
func percentOrNil(raw string) *kapsorav1.LodgingPercent {
	if raw == "" {
		return nil
	}
	value := raw
	return &value
}
