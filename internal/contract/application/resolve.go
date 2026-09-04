package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// ResolveRequest is one price lookup.
type ResolveRequest struct {
	ServiceDate         time.Time
	ProviderProfileID   uuid.UUID
	ServiceDefinitionID uuid.UUID
	LocationID          *uuid.UUID
}

// ResolvedCandidate is one scored candidate together with the money it carries, so a
// screen can explain the choice rather than assert it.
type ResolvedCandidate struct {
	Scored selection.Scored
	Detail PriceDetail
}

// ResolveResult is the outcome of one price lookup. It never contains a winner picked
// among equals: two candidates that tie leave Winner nil and Reason PRICE_AMBIGUOUS, and
// the caller has to fix the configuration.
type ResolveResult struct {
	ServiceDate time.Time
	Winner      *ResolvedCandidate
	Reason      selection.Reason
	Tied        []ResolvedCandidate
	Considered  []ResolvedCandidate
}

// Outcome renders the result as the contract's outcome word.
func (r ResolveResult) Outcome() string {
	switch {
	case r.Winner != nil:
		return "MATCHED"
	case r.Reason == selection.ReasonAmbiguous:
		return "REVIEW_REQUIRED"
	default:
		return "NOT_FOUND"
	}
}

// ResolvePrice answers which contracted price applies to a service, at a provider, on a
// date. It is a read: the loaded rows are handed to the pure selection function, which
// decides, and the only thing this method adds is the two lookups the request needs (the
// category chain above the definition and the packages containing it) and the money the
// winner is reported with.
func (s *Service) ResolvePrice(ctx context.Context, rc identity.RequestContext, in ResolveRequest) (ResolveResult, error) {
	ve := &domain.ValidationError{}
	if in.ServiceDate.IsZero() {
		ve.Add("serviceDate", "REQUIRED", "zorunlu alan")
	}
	if in.ProviderProfileID == uuid.Nil {
		ve.Add("providerProfileId", "REQUIRED", "zorunlu alan")
	}
	if in.ServiceDefinitionID == uuid.Nil {
		ve.Add("serviceDefinitionId", "REQUIRED", "zorunlu alan")
	}
	if err := ve.OrNil(); err != nil {
		return ResolveResult{}, err
	}
	serviceDate := domain.DateOnly(in.ServiceDate)

	out := ResolveResult{ServiceDate: serviceDate}
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		exists, err := s.repo.ServiceDefinitionExists(ctx, tx, rc.TenantID, in.ServiceDefinitionID)
		if err != nil {
			return err
		}
		if !exists {
			return fieldError("serviceDefinitionId", "NOT_FOUND", "hizmet tanımı bulunamadı")
		}
		// The nearest category first and then each ancestor: the ladder scores a nearer
		// ancestor above a further one, so the order of this slice is the ladder.
		categories, err := s.repo.CategoryPath(ctx, tx, rc.TenantID, in.ServiceDefinitionID)
		if err != nil {
			return err
		}
		packages, err := s.repo.PackagesContaining(ctx, tx, rc.TenantID, in.ServiceDefinitionID)
		if err != nil {
			return err
		}
		candidates, err := s.repo.ListCandidates(ctx, tx, rc.TenantID, CandidateQuery{
			ProviderProfileID: in.ProviderProfileID, ServiceDate: serviceDate,
			ServiceDefinitionID: in.ServiceDefinitionID, CategoryIDs: categories, PackageIDs: packages,
		})
		if err != nil {
			return err
		}

		details := make(map[uuid.UUID]PriceDetail, len(candidates))
		rows := make([]selection.Candidate, 0, len(candidates))
		for _, c := range candidates {
			details[c.Candidate.PriceItemID] = c.Detail
			rows = append(rows, c.Candidate)
		}
		req := selection.Request{
			ServiceDate: serviceDate, DefinitionID: in.ServiceDefinitionID,
			CategoryPath: categories, PackagesContaining: packages,
		}
		if in.LocationID != nil {
			req.LocationID = *in.LocationID
		}
		result := selection.Select(req, rows)

		// Considered carries every candidate with the score the ladder gave it, so it is
		// also where the winner's and the tied rows' scores come from.
		scores := make(map[uuid.UUID]selection.Scored, len(result.Considered))
		for _, scored := range result.Considered {
			scores[scored.Candidate.PriceItemID] = scored
			out.Considered = append(out.Considered, ResolvedCandidate{
				Scored: scored, Detail: details[scored.Candidate.PriceItemID],
			})
		}
		out.Reason = result.Reason
		if result.Winner != nil {
			winner := ResolvedCandidate{
				Scored: scores[result.Winner.PriceItemID], Detail: details[result.Winner.PriceItemID],
			}
			out.Winner = &winner
		}
		for _, tied := range result.Tied {
			out.Tied = append(out.Tied, ResolvedCandidate{
				Scored: scores[tied.PriceItemID], Detail: details[tied.PriceItemID],
			})
		}
		return nil
	})
	if err != nil {
		return ResolveResult{}, err
	}
	return out, nil
}
