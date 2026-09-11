package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// DecisionInput is one per-line decision as the reviewer sent it.
type DecisionInput struct {
	LineNo           int
	Decision         string
	ApprovedQuantity string
	ApprovedAmount   string
	PayerAmount      string
	MemberAmount     string
	ReasonCode       string
	ReasonText       *string
}

// DecideInput is the whole line-decision command.
type DecideInput struct {
	Decisions       []DecisionInput
	ReviewComment   *string
	ExpectedVersion int64
}

// DecideLines records a decision on each named line of the current version, at the stage the
// claim is waiting in.
//
// **The stage is the claim's, not the caller's.** A claim in PENDING_MEDICAL is decided
// MEDICAL and a claim in PENDING_FINANCIAL is decided FINANCIAL, and a reviewer holding the
// other grant is answered 409 rather than being allowed to write a decision at the stage they
// happen to hold. That is what makes medical review *precede* financial rather than merely
// usually happen first: the financial reviewer cannot reach a claim the medical reviewer has
// not finished, and so cannot decide a clinical line's reason.
//
// Decisions are append-only. A line decided twice has two rows and the latest is the decision,
// which is what lets a reviewer who cut a line and a reviewer who later restored it both stay
// on the record.
func (s *Service) DecideLines(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in DecideInput, req AccessRequest,
) (ClaimView, error) {
	if err := domain.ValidateDecisions(decisionRows(in.Decisions)); err != nil {
		return ClaimView{}, err
	}
	if err := domain.ValidateReviewComment(in.ReviewComment); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		// A reviewer may not decide a file that belongs to their own person.
		if err := identity.RefuseOwnFile(rc, current.PersonID); err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandDecide, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		stage, ok := domain.StageFor(current.Status)
		if !ok {
			return ErrTransitionInvalid
		}
		if !holdsStage(rc, stage) {
			// The caller may review claims, but not at the stage this one is waiting in.
			return ErrStageMismatch
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		byLineNo := make(map[int]LineRecord, len(lines))
		for _, line := range lines {
			byLineNo[line.LineNo] = line
		}

		now := s.now().UTC()
		actor := actorPtr(rc.Principal.ActorID)
		for _, decision := range in.Decisions {
			line, ok := byLineNo[decision.LineNo]
			if !ok {
				return ErrLineNotFound
			}
			if _, err := s.repo.CreateDecision(ctx, tx, rc.TenantID, NewDecisionRow{
				LineID: line.ID, DecidedInVersionNo: version.VersionNo,
				Decision:         decision.Decision,
				ApprovedQuantity: decision.ApprovedQuantity,
				ApprovedAmount:   decision.ApprovedAmount,
				PayerAmount:      decision.PayerAmount, MemberAmount: decision.MemberAmount,
				ReasonCode: decision.ReasonCode, ReasonText: trimmedPtr(decision.ReasonText),
				DecidedBy: actor, DecidedAt: now, Stage: stage,
			}); err != nil {
				return err
			}
			// A CUT is not written to `claim.adjustment` here, and that is a decision
			// WP-I7-01 made rather than a step somebody forgot. The line decision *is* the
			// cut: it records the reduced approved amount with the reviewer, the stage and
			// the reason. `claim.adjustment` is the ledger of money that moved **outside**
			// a line decision, which is exactly what makes "approved total = lines minus
			// adjustments" a sum nobody counts twice. Writing both would subtract the same
			// hundred lira from the same claim in two places, and the provider would be
			// paid the difference of a bug.
		}
		if in.ReviewComment != nil {
			if err := s.repo.SetReviewComment(ctx, tx, rc.TenantID, id, stage,
				trimmedPtr(in.ReviewComment), actor); err != nil {
				return err
			}
		}

		// The claim moves on only when every line of the version carries a decision, and
		// only into the stage the frozen snapshot said was still owed.
		if err := s.advanceStage(ctx, tx, rc, current, version, stage); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "claim.lines.decide", id, map[string]any{
			"reference": current.Reference, "version_no": version.VersionNo,
			"stage": stage, "decision_count": len(in.Decisions),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// advanceStage moves a claim whose medical review is finished into financial review, when the
// submit said financial was also owed.
//
// It reads the flag from the frozen snapshot rather than from a column, because the snapshot
// is the record of what the pipeline decided and a column would be a second copy of it that
// could disagree. A claim that needs only one stage stays where it is until somebody approves,
// rejects or returns it.
func (s *Service) advanceStage(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, stage string,
) error {
	if stage != domain.StageMedical {
		return nil
	}
	financialRequired, _ := readRouting(version.Snapshot)
	if !financialRequired {
		return nil
	}
	decided, err := s.allLinesDecided(ctx, tx, rc.TenantID, version.ID)
	if err != nil || !decided {
		return err
	}
	// The row version has moved: the decisions above were written under it and the reviewer
	// is holding an ETag from before them. The status move therefore reads the row again
	// rather than re-using the caller's If-Match, which has already done its job.
	fresh, err := s.repo.LockClaim(ctx, tx, rc.TenantID, record.ID, Scope{})
	if err != nil {
		return err
	}
	moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, record.ID, StatusRow{
		Status: domain.StatusPendingFinancial, CurrentVersionNo: version.VersionNo,
		FromStatuses: []string{domain.StatusPendingMedical},
		ActorID:      actorPtr(rc.Principal.ActorID),
	}, fresh.RowVersion)
	if err != nil || !moved {
		return err
	}
	return s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
		QueueCode: QueueFinancialReview, AggregateType: domain.AggregateClaim,
		AggregateID: record.ID, Title: "Hasar dosyası " + record.Reference,
		ActorID: actorPtr(rc.Principal.ActorID),
	})
}

// Approve finishes the claim. The status it lands in is what its line decisions say, never
// what the caller asked for: a reviewer does not choose between "approved" and "partially
// approved", the lines they decided do.
func (s *Service) Approve(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (ClaimView, error) {
	if err := domain.ValidateReason("reasonCode", in.ReasonCode, in.ReasonText); err != nil {
		return ClaimView{}, err
	}
	if err := domain.ValidateReviewComment(in.ReviewComment); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		// A reviewer may not decide a file that belongs to their own person.
		if err := identity.RefuseOwnFile(rc, current.PersonID); err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandApprove, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		decisions, err := s.repo.ListLatestDecisions(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		if len(decisions) != len(lines) {
			return ErrLineUndecided
		}
		totals := sumDecisions(decisions)
		if err := s.checkApprovalPolicy(ctx, rc, current, totals.Approved); err != nil {
			return err
		}

		status := statusFor(decisions)
		now := s.now().UTC()
		var closedAt *time.Time
		if domain.Closed(status) {
			closedAt = &now
		}
		row := StatusRow{
			Status: status, CurrentVersionNo: version.VersionNo, ClosedAt: closedAt,
			FromStatuses: domain.From(domain.CommandApprove), ActorID: actorPtr(rc.Principal.ActorID),
		}
		if status == domain.StatusRejected {
			code := in.ReasonCode
			row.RejectReasonCode = &code
		}
		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, row, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}
		if in.ReviewComment != nil {
			stage := stageOf(rc)
			if err := s.repo.SetReviewComment(ctx, tx, rc.TenantID, id, stage,
				trimmedPtr(in.ReviewComment), actorPtr(rc.Principal.ActorID)); err != nil {
				return err
			}
		}
		// A claim approved for less than it reserved gives the difference back. The member
		// was promised the entitlement and did not use all of it.
		if err := s.releaseHold(ctx, tx, rc, current, "CLAIM_APPROVED"); err != nil {
			return err
		}
		if err := s.notifyDecided(ctx, tx, rc, current, status, totals,
			currencyOf(lines), now); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "claim.approve", id, map[string]any{
			"reference": current.Reference, "version_no": version.VersionNo,
			"status": status, "reason_code": in.ReasonCode,
			"approved_total": totals.Approved.String(), "payer_total": totals.Payer.String(),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// Reject refuses the claim outright, with a reason.
//
// Every line of the current version is recorded REJECTED at the caller's stage, whatever it
// carried before. That is not bookkeeping: the decisions are what an invoice, an appeal and a
// settlement read, and a claim rejected as a whole while its lines still said "approved" would
// be a claim two systems disagreed about.
func (s *Service) Reject(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (ClaimView, error) {
	if err := domain.ValidateReason("reasonCode", in.ReasonCode, in.ReasonText); err != nil {
		return ClaimView{}, err
	}
	if err := domain.ValidateReviewComment(in.ReviewComment); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		// A reviewer may not decide a file that belongs to their own person.
		if err := identity.RefuseOwnFile(rc, current.PersonID); err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandReject, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		stage := stageOf(rc)
		for _, line := range lines {
			if _, err := s.repo.CreateDecision(ctx, tx, rc.TenantID, NewDecisionRow{
				LineID: line.ID, DecidedInVersionNo: version.VersionNo,
				Decision:         domain.DecisionRejected,
				ApprovedQuantity: zero().String(), ApprovedAmount: zero().String(),
				PayerAmount: zero().String(), MemberAmount: zero().String(),
				ReasonCode: in.ReasonCode, ReasonText: trimmedPtr(in.ReasonText),
				DecidedBy: actorPtr(rc.Principal.ActorID), DecidedAt: now, Stage: stage,
			}); err != nil {
				return err
			}
		}
		code := in.ReasonCode
		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusRow{
			Status: domain.StatusRejected, CurrentVersionNo: version.VersionNo,
			RejectReasonCode: &code, ClosedAt: &now,
			FromStatuses: domain.From(domain.CommandReject), ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}
		if in.ReviewComment != nil {
			if err := s.repo.SetReviewComment(ctx, tx, rc.TenantID, id, stage,
				trimmedPtr(in.ReviewComment), actorPtr(rc.Principal.ActorID)); err != nil {
				return err
			}
		}
		// A refused claim must not keep a member's entitlement reserved.
		if err := s.releaseHold(ctx, tx, rc, current, "CLAIM_REJECTED"); err != nil {
			return err
		}
		if err := s.notifyDecided(ctx, tx, rc, current, domain.StatusRejected,
			pipelineTotals{Approved: zero(), Payer: zero(), Member: zero()},
			currencyOf(lines), now); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "claim.reject", id, map[string]any{
			"reference": current.Reference, "version_no": version.VersionNo,
			"reason_code": in.ReasonCode, "stage": stage, "line_count": len(lines),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// Return sends the claim back to the provider to be corrected.
//
// **It never edits the decided version.** The submitted version becomes SUPERSEDED with the
// return reason on it and keeps every line decision it was given; version n+1 is opened as a
// DRAFT with the lines copied. That is the whole correction model, and it is the same one
// WP-I4-01 gave the service request — a provider who has learned how a returned request comes
// back must not have to learn a different rule for a returned claim.
func (s *Service) Return(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (ClaimView, error) {
	if err := domain.ValidateReason("reasonCode", in.ReasonCode, in.ReasonText); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		// A reviewer may not decide a file that belongs to their own person.
		if err := identity.RefuseOwnFile(rc, current.PersonID); err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandReturn, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		actor := actorPtr(rc.Principal.ActorID)
		superseded, err := s.repo.SupersedeVersion(ctx, tx, rc.TenantID, version.ID, ReturnRow{
			ReturnedAt: now, ReasonCode: in.ReasonCode, ReasonText: trimmedPtr(in.ReasonText),
			ActorID: actor,
		})
		if err != nil {
			return err
		}
		if !superseded {
			return ErrTransitionInvalid
		}
		next, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, id, version.VersionNo+1, actor)
		if err != nil {
			return err
		}
		// The lines are copied, clinical fields and all: the provider is correcting its own
		// statement, and a correction that silently dropped what a line named would be a
		// correction that changed more than the provider meant.
		if err := s.repo.ReplaceLines(ctx, tx, rc.TenantID, next.ID,
			copyLines(next.ID, lines, actor)); err != nil {
			return err
		}
		code := in.ReasonCode
		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusRow{
			Status: domain.StatusReturned, CurrentVersionNo: next.VersionNo,
			ReturnReasonCode: &code, FromStatuses: domain.From(domain.CommandReturn),
			ActorID: actor,
		}, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}
		if err := s.record(ctx, tx, rc, "claim.return", id, map[string]any{
			"reference": current.Reference, "returned_version_no": version.VersionNo,
			"new_version_no": next.VersionNo, "reason_code": in.ReasonCode,
			"line_count": len(lines),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// checkApprovalPolicy asks WP-I4-03 section 2.4 which roles may finish a claim of this amount.
//
// A band that names no role lets either reviewer finish, which is what a tenant that has
// configured an approver count but not a role list has said. A band that names roles admits
// only a caller whose review stage is one of them — the stage rather than the role, because
// this package knows which grant the caller holds and not which template it was issued under.
func (s *Service) checkApprovalPolicy(ctx context.Context, rc identity.RequestContext,
	record ClaimRecord, approved benefitdomain.Quantity,
) error {
	policy, err := s.policies.Resolve(ctx, rc, PolicyLookup{
		ActionCode: ActionCodeApprove, Amount: approved.String(), AsOf: record.ServiceDateFrom,
	})
	if err != nil {
		return err
	}
	if !policy.Found || len(policy.RequiredRoleCodes) == 0 {
		return nil
	}
	for _, role := range policy.RequiredRoleCodes {
		if stageRoles[role] == "" {
			continue
		}
		if holdsStage(rc, stageRoles[role]) {
			return nil
		}
	}
	return ErrApprovalNotPermitted
}

// stageRoles maps the role codes an approval policy names onto the review stage each of them
// reviews at. A policy naming a role this package knows nothing about — PAYER_APPROVER, say —
// is simply not satisfied by either reviewer, which is the honest reading of a band that asks
// for somebody else.
var stageRoles = map[string]string{
	"MEDICAL_REVIEWER":   domain.StageMedical,
	"FINANCIAL_REVIEWER": domain.StageFinancial,
}

// holdsStage reports whether the caller holds the grant a stage is decided with.
func holdsStage(rc identity.RequestContext, stage string) bool {
	switch stage {
	case domain.StageMedical:
		return rc.Has(PermissionMedicalReview)
	case domain.StageFinancial:
		return rc.Has(PermissionFinancialReview)
	default:
		return false
	}
}

// stageOf is the stage a claim-level command records its line decisions at: the caller's own,
// medical first when they hold both. A caller holding neither review grant cannot reach these
// commands at all — the route requires one — so FINANCIAL is only ever the fallback of a
// caller that holds it.
func stageOf(rc identity.RequestContext) string {
	if rc.Has(PermissionMedicalReview) {
		return domain.StageMedical
	}
	return domain.StageFinancial
}

// allLinesDecided reports whether every line of a version carries a decision.
func (s *Service) allLinesDecided(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (bool, error) {
	lines, err := s.repo.ListLines(ctx, tx, tenantID, versionID)
	if err != nil {
		return false, err
	}
	decisions, err := s.repo.ListLatestDecisions(ctx, tx, tenantID, versionID)
	if err != nil {
		return false, err
	}
	return len(decisions) == len(lines), nil
}

// sumDecisions totals the line decisions, on the server, once. Every figure is an exact
// decimal the whole way; nothing here becomes a float and nothing is added up by a frontend.
func sumDecisions(decisions []DecisionRecord) pipelineTotals {
	out := pipelineTotals{Approved: zero(), Payer: zero(), Member: zero()}
	for _, d := range decisions {
		out.Approved = out.Approved.Add(quantityOrZero(d.ApprovedAmount))
		out.Payer = out.Payer.Add(quantityOrZero(d.PayerAmount))
		out.Member = out.Member.Add(quantityOrZero(d.MemberAmount))
	}
	return out
}

// statusFor is what the decisions say the claim is: approved when every line was approved,
// rejected when every line was refused, partially approved in between.
func statusFor(decisions []DecisionRecord) string {
	approved, rejected := 0, 0
	for _, d := range decisions {
		if d.Decision == domain.DecisionRejected {
			rejected++
			continue
		}
		approved++
	}
	switch {
	case approved == 0:
		return domain.StatusRejected
	case rejected > 0:
		return domain.StatusPartiallyApproved
	default:
		return domain.StatusApproved
	}
}

// currencyOf is the currency a claim's lines are in. The domain refuses a mixed set at the
// door, so the first line's currency is the claim's.
func currencyOf(lines []LineRecord) string {
	if len(lines) == 0 {
		return "TRY"
	}
	return lines[0].CurrencyCode
}

// decisionRows maps the wire shape onto the domain's.
func decisionRows(in []DecisionInput) []domain.Decision {
	out := make([]domain.Decision, 0, len(in))
	for _, d := range in {
		out = append(out, domain.Decision{
			LineNo: d.LineNo, Decision: d.Decision, ApprovedQuantity: d.ApprovedQuantity,
			ApprovedAmount: d.ApprovedAmount, PayerAmount: d.PayerAmount,
			MemberAmount: d.MemberAmount, ReasonCode: d.ReasonCode, ReasonText: d.ReasonText,
		})
	}
	return out
}
