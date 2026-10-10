package authorizationpg

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/authorization/application"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ResolveEntitlements reads the request enrollment's published plan on its service date.
// An explicit mapping wins over the old service-code convention, including its factor.
func (r *Repository) ResolveEntitlements(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID,
	day time.Time, services []uuid.UUID,
) (map[uuid.UUID]application.EntitlementTarget, error) {
	q := sqlcgen.New(tx)
	enrollment, err := q.GetEnrollmentForEntitlement(ctx, sqlcgen.GetEnrollmentForEntitlementParams{TenantID: tenantID, ID: enrollmentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	version, err := benefitapp.ResolvePlanVersion(ctx, tx, tenantID, enrollment.PlanID, day)
	if errors.Is(err, benefitapp.ErrNoPublishedVersion) {
		return nil, application.ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	definitions, err := q.ListEntitlementDefinitionsForMapping(ctx, sqlcgen.ListEntitlementDefinitionsForMappingParams{
		TenantID: tenantID, PlanVersionID: version.ID,
	})
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]uuid.UUID, len(definitions))
	for _, d := range definitions {
		byCode[d.Code] = d.ID
	}
	codes, err := r.ServiceDefinitionCodes(ctx, tx, tenantID, services)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]application.EntitlementTarget, len(services))
	for id, code := range codes {
		if definition, ok := byCode[code]; ok {
			out[id] = application.EntitlementTarget{DefinitionID: definition, Factor: benefitdomain.MustQuantity("1")}
		}
	}
	mappings, err := q.ListEligibilityMappings(ctx, sqlcgen.ListEligibilityMappingsParams{
		TenantID: tenantID, PlanVersionID: version.ID, ServiceDate: pgtype.Date{Time: day, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	for _, m := range mappings {
		factor, err := benefitdomain.ParseQuantity(m.UnitFactor)
		if err != nil {
			return nil, err
		}
		out[m.ServiceDefinitionID] = application.EntitlementTarget{DefinitionID: byCode[m.EntitlementCode], Factor: factor}
	}
	return out, nil
}
