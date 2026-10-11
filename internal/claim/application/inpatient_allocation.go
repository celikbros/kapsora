package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

const inpatientStaySource = "INPATIENT_STAY"

func isInpatientStayClaim(record ClaimRecord) bool {
	return record.SourceType != nil && *record.SourceType == inpatientStaySource
}

func (s *Service) inpatientPlan(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, record ClaimRecord) (InpatientStayPlan, error) {
	if record.SourceID == nil || record.CaseID == nil || record.AuthorizationID == nil {
		return InpatientStayPlan{}, nil
	}
	return s.repo.InpatientStayPlan(ctx, tx, rc.TenantID, *record.SourceID,
		*record.CaseID, record.ProviderOrganizationID, *record.AuthorizationID)
}

func stayPlanComplete(plan InpatientStayPlan, outcomes []lineOutcome) bool {
	if !plan.Found || !plan.Discharged || plan.OverAuthorization || !plan.ActualDays.IsPositive() ||
		len(plan.Holds) == 0 || len(outcomes) != 1 {
		return false
	}
	if outcomes[0].line.ServiceCode == nil || *outcomes[0].line.ServiceCode != "INPATIENT_DAY" {
		return false
	}
	quantity, err := quantityOf(outcomes[0].line.Quantity, "quantity")
	if err != nil || quantity.Cmp(plan.ActualDays) != 0 {
		return false
	}
	total := benefitdomain.ZeroQuantity()
	for _, hold := range plan.Holds {
		if !hold.Days.IsPositive() || hold.AuthorizationID == uuid.Nil {
			return false
		}
		total = total.Add(hold.Days)
	}
	return total.Cmp(plan.ActualDays) >= 0
}

func deferInpatientDecision(outcome *lineOutcome) {
	if outcome.decision == domain.DecisionRejected {
		return
	}
	outcome.decision = ""
	outcome.approvedQuantity = zero()
	outcome.approvedAmount = zero()
	outcome.payerAmount = zero()
	outcome.memberAmount = zero()
	outcome.needsMedical = true
}

func stayOver(outcome *lineOutcome) {
	deferInpatientDecision(outcome)
	for _, exception := range outcome.exceptions {
		if exception.Code == ReasonStayOverAuthorization {
			return
		}
	}
	outcome.exceptions = append(outcome.exceptions, ClaimException{
		LineNo: outcome.line.LineNo, Code: ReasonStayOverAuthorization, Stage: "MEDICAL",
	})
}

func (s *Service) crossCheckInpatient(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, outcomes []lineOutcome,
) error {
	plan, err := s.inpatientPlan(ctx, tx, rc, record)
	if err != nil {
		return err
	}
	if !stayPlanComplete(plan, outcomes) {
		for i := range outcomes {
			stayOver(&outcomes[i])
		}
		return nil
	}
	for i := range outcomes {
		outcome := &outcomes[i]
		if outcome.decision == domain.DecisionRejected {
			continue
		}
		if err := s.consumeInpatientLine(ctx, tx, rc, outcome, plan); err != nil {
			return err
		}
		if err := s.checkReport(ctx, tx, rc, record, outcome); err != nil {
			return err
		}
		if err := s.checkDuplicate(ctx, tx, rc, record, outcome); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) consumeInpatientLine(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	outcome *lineOutcome, plan InpatientStayPlan,
) error {
	quantity, err := quantityOf(outcome.line.Quantity, "quantity")
	if err != nil {
		return err
	}
	remaining := quantity
	rows := make([]LineAllocation, 0, len(plan.Holds))
	draws := make([]ConsumptionAllocation, 0, len(plan.Holds))
	for i, hold := range plan.Holds {
		if remaining.IsZero() {
			break
		}
		planned := remaining.Min(hold.Days)
		key := fmt.Sprintf("claim-line:%s:authorization:%s", outcome.line.ID, hold.AuthorizationID)
		rows = append(rows, LineAllocation{
			VersionID: outcome.line.VersionID, LineID: outcome.line.ID,
			AuthorizationID: hold.AuthorizationID, ServiceDefinitionID: outcome.line.ServiceDefinitionID,
			Order: i + 1, Planned: planned, Key: key,
		})
		draws = append(draws, ConsumptionAllocation{AuthorizationID: hold.AuthorizationID, Quantity: planned, Key: key})
		remaining = remaining.Sub(planned)
	}
	if !remaining.IsZero() {
		stayOver(outcome)
		return nil
	}
	answer, err := s.authorizations.ConsumeAllocations(ctx, tx, ConsumeAllocationsRequest{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		ServiceDefinitionID: outcome.line.ServiceDefinitionID, Quantity: quantity,
		ReasonCode: consumeReason, Allocations: draws,
	})
	if err != nil {
		return err
	}
	if !answer.OverConsumed {
		if answer.Consumed.Cmp(quantity) != 0 || len(answer.Draws) != len(draws) {
			return fmt.Errorf("claim: inpatient allocation receipt does not match planned quantity")
		}
		received := make(map[string]ConsumptionAllocation, len(answer.Draws))
		for _, draw := range answer.Draws {
			if _, exists := received[draw.Key]; exists {
				return fmt.Errorf("claim: duplicate authorization draw receipt")
			}
			received[draw.Key] = draw
		}
		for _, planned := range draws {
			draw, ok := received[planned.Key]
			if !ok || draw.AuthorizationID != planned.AuthorizationID ||
				draw.Quantity.Cmp(planned.Quantity) != 0 {
				return fmt.Errorf("claim: inpatient allocation receipt differs from plan")
			}
		}
	} else if !answer.Consumed.IsZero() || len(answer.Draws) != 0 {
		return fmt.Errorf("claim: over-consumption returned applied draws")
	}
	for _, row := range rows {
		if !answer.OverConsumed {
			row.Applied = row.Planned
		}
		if err := s.repo.CreateLineAllocation(ctx, tx, rc.TenantID, row); err != nil {
			return err
		}
	}
	if answer.OverConsumed {
		deferInpatientDecision(outcome)
		outcome.exceptions = append(outcome.exceptions, ClaimException{
			LineNo: outcome.line.LineNo, Code: ReasonAuthorizationExceeded,
			Stage: "MEDICAL", Detail: answer.Remaining.String(),
		})
	}
	return nil
}

// inpatientAllocationComplete verifies a submitted version has the full immutable receipt
// before a positive decision can expose it to billing.
func (s *Service) inpatientAllocationComplete(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, versionID uuid.UUID, lines []LineRecord,
) (bool, error) {
	if len(lines) != 1 || lines[0].ServiceCode == nil || *lines[0].ServiceCode != "INPATIENT_DAY" {
		return false, nil
	}
	plan, err := s.inpatientPlan(ctx, tx, rc, record)
	if err != nil {
		return false, err
	}
	quantity, err := quantityOf(lines[0].Quantity, "quantity")
	if err != nil {
		return false, err
	}
	if !plan.Found || !plan.Discharged || plan.OverAuthorization || quantity.Cmp(plan.ActualDays) != 0 {
		return false, nil
	}
	allocations, err := s.repo.ListVersionAllocations(ctx, tx, rc.TenantID, versionID)
	if err != nil {
		return false, err
	}
	remaining := quantity
	expected := 0
	for _, hold := range plan.Holds {
		if remaining.IsZero() {
			break
		}
		planned := remaining.Min(hold.Days)
		if !planned.IsPositive() || expected >= len(allocations) {
			return false, nil
		}
		allocation := allocations[expected]
		key := fmt.Sprintf("claim-line:%s:authorization:%s", lines[0].ID, hold.AuthorizationID)
		if allocation.LineID != lines[0].ID ||
			allocation.ServiceDefinitionID != lines[0].ServiceDefinitionID ||
			allocation.AuthorizationID != hold.AuthorizationID || allocation.Order != expected+1 ||
			allocation.Key != key || allocation.Planned.Cmp(planned) != 0 ||
			allocation.Applied.Cmp(planned) != 0 {
			return false, nil
		}
		expected++
		remaining = remaining.Sub(planned)
	}
	return remaining.IsZero() && len(allocations) == expected, nil
}
