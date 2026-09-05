// Package healthgw is where the inpatient stay meets the two modules it cannot do without:
// the service request its preauthorization is (WP-I4-01) and the authorization its approval
// produces (WP-I4-02).
//
// It exists so that neither of those modules knows an admission exists and the health module
// holds no copy of what they do. The ports are declared in health/application in this
// package's own vocabulary — days, an admission window, a release — and the two adapters
// below are the only code in the repository that speaks both languages. That is the whole of
// the coupling: about a hundred lines, in one file, that a reader can hold in their head.
//
// Nothing here decides anything. Every refusal comes from the module being called: the
// eligibility gate is WP-I4-01's, the no-double-spend rule is the ledger's, and the window a
// promise is extended into is WP-I4-02's to validate. An adapter that added a rule would be a
// rule nobody reading either module could find.
package healthgw

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The request an admission is asked for with. Both values are WP-I4-01's own vocabulary and
// are spelled here rather than passed in: a stay is a preauthorization, always, and a caller
// that could choose the type could book an admission as a reimbursement.
const preauthorizationType = "PREAUTHORIZATION"

// Requests turns "ask for these days" into WP-I4-01's create and submit, in the caller's
// transaction. Both halves happen here because an admission is asked for and handed over in
// one act: a stay whose request sat in DRAFT would be a stay no reviewer would ever see.
type Requests struct{ svc *servicerequestapp.Service }

// NewRequests wraps the service request module.
func NewRequests(svc *servicerequestapp.Service) *Requests { return &Requests{svc: svc} }

var _ healthapp.RequestPort = (*Requests)(nil)

// CreatePreauthorization implements healthapp.RequestPort.
//
// The line is one line for the whole admission, with the day count as its quantity, rather
// than one line per day. A reviewer decides "four of the five days" by reducing a quantity,
// which is the decision WP-I4-01 already knows how to record; five separate lines would make
// the same decision five approvals and would put a hundred-line request in front of anybody
// admitting somebody for a fortnight.
func (r *Requests) CreatePreauthorization(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in healthapp.StayRequestInput,
) (healthapp.StayRequestRef, error) {
	provider := in.ProviderOrganizationID
	start, end := in.RequestedStartAt, in.RequestedEndAt
	draft, err := r.svc.CreateInTx(ctx, tx, rc, servicerequestapp.NewRequestInput{
		RequestType: preauthorizationType, PersonID: in.PersonID,
		EnrollmentID: in.EnrollmentID, ProviderOrganizationID: &provider,
		ServiceDate: in.ServiceDate, RequestedStartAt: &start, RequestedEndAt: &end,
		Channel: channelOf(rc),
		Items: []servicerequestdomain.ItemInput{{
			ServiceDefinitionID: in.ServiceDefinitionID.String(),
			RequestedQuantity:   strconv.Itoa(in.Days),
			UnitType:            in.UnitType,
		}},
	})
	if err != nil {
		return healthapp.StayRequestRef{}, err
	}
	// Straight through the gate. Whatever it decides — approved outright, waiting for a
	// document, refused on eligibility — is what the stay's request page will show, and
	// this package neither reads that decision nor acts on it: the outbox does.
	submitted, err := r.svc.SubmitInTx(ctx, tx, rc, draft.Request.ID, nil, draft.Request.RowVersion)
	if err != nil {
		return healthapp.StayRequestRef{}, err
	}
	return healthapp.StayRequestRef{
		ID: submitted.Request.ID, Reference: submitted.Request.Reference,
		Status: submitted.Request.Status,
	}, nil
}

// channelOf says where the request came from. A provider-scoped caller is at a hospital desk
// and a tenant-wide one is in the back office; the channel is what a later report counts, so
// guessing one for everybody would make the count meaningless.
func channelOf(rc identity.RequestContext) string {
	for _, scope := range rc.Scopes {
		if scope.Type == healthapp.ScopeOrganization {
			return "PROVIDER_PORTAL"
		}
	}
	return "BACKOFFICE"
}

// Authorizations is WP-I4-02 seen from the health module: take a hold for what a request
// promised, move a hold's end forward, and give back what was never used.
type Authorizations struct{ svc *authorizationapp.Service }

// NewAuthorizations wraps the authorization module.
func NewAuthorizations(svc *authorizationapp.Service) *Authorizations {
	return &Authorizations{svc: svc}
}

var _ healthapp.AuthorizationPort = (*Authorizations)(nil)

// CreateForRequest implements healthapp.AuthorizationPort.
//
// The approved day count it answers with is the sum of what the authorization's lines
// actually promised, not what the request asked for. That distinction is the whole of the
// reconciliation: a reviewer who approved four of five days has authorized four, and a stay
// that recorded five would release one day too few at discharge and hold entitlement nobody
// ever promised.
func (a *Authorizations) CreateForRequest(ctx context.Context, rc identity.RequestContext,
	in healthapp.StayAuthorizationInput,
) (healthapp.StayAuthorizationRef, error) {
	validFrom := in.ValidFrom
	view, err := a.svc.Create(ctx, rc, authorizationapp.NewAuthorizationInput{
		RequestID: in.RequestID, ValidFrom: &validFrom, ValidTo: in.ValidTo,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return healthapp.StayAuthorizationRef{}, err
	}
	approved := benefitdomain.ZeroQuantity()
	for _, item := range view.Items {
		quantity, err := benefitdomain.ParseQuantity(item.ApprovedQuantity)
		if err != nil {
			return healthapp.StayAuthorizationRef{},
				fmt.Errorf("health: authorization line %s quantity: %w", item.ID, err)
		}
		approved = approved.Add(quantity)
	}
	return healthapp.StayAuthorizationRef{
		ID: view.Authorization.ID, ApprovedDays: approved.String(),
		ValidTo: view.Authorization.ValidTo, RowVersion: view.Authorization.RowVersion,
	}, nil
}

// ExtendValidity implements healthapp.AuthorizationPort.
//
// It reads the authorization first and does nothing when it already ends late enough. That is
// not an optimisation: the outbox delivers at least once, WP-I4-02 refuses an extension that
// does not move the end forward, and without this check a redelivered extension approval
// would fail on every retry until it dead-lettered. Reading first turns "already done" into
// success, which is what an idempotent handler needs it to be.
func (a *Authorizations) ExtendValidity(ctx context.Context, rc identity.RequestContext,
	authorizationID uuid.UUID, validTo time.Time, reasonCode string,
) error {
	view, err := a.svc.Get(ctx, rc, authorizationID)
	if err != nil {
		return err
	}
	if !validTo.After(view.Authorization.ValidTo) {
		return nil
	}
	_, err = a.svc.Extend(ctx, rc, authorizationID, authorizationapp.ExtendInput{
		ValidTo: validTo, ReasonCode: reasonCode,
		ExpectedVersion: view.Authorization.RowVersion,
	})
	if errors.Is(err, authorizationapp.ErrAuthorizationNotActive) {
		// The promise has already expired or been withdrawn. There is nothing to extend and
		// nothing to fix: the extension's own authorization holds the added days, and a
		// handler that failed here would retry forever against a hold that is gone.
		return nil
	}
	return err
}

// ReleaseUnused implements healthapp.AuthorizationPort. It runs in the caller's transaction,
// so a discharge and the entitlement it gives back commit together or not at all.
func (a *Authorizations) ReleaseUnused(ctx context.Context, tx pgx.Tx,
	in healthapp.StayReleaseInput,
) (string, error) {
	quantity, err := benefitdomain.ParseQuantity(in.Days)
	if err != nil {
		return "", fmt.Errorf("health: release quantity %q: %w", in.Days, err)
	}
	released, err := a.svc.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
		TenantID: in.TenantID, ActorID: in.ActorID, AuthorizationID: in.AuthorizationID,
		Quantity: quantity, ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return "", err
	}
	return released.String(), nil
}
