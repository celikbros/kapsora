package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// ReasonInput is the body of return, reject and cancel: why, in a code somebody can count,
// and optionally in words somebody can read.
type ReasonInput struct {
	ReasonCode      string
	ReasonText      *string
	ExpectedVersion int64
}

// DecisionInput is the body of approve and partiallyApprove.
type DecisionInput struct {
	ReasonCode      string
	ReasonText      *string
	Items           []domain.DecisionItem
	ExpectedVersion int64
}

// Return sends a request back to the requester. It is not a rejection: the request keeps
// its reference, the frozen version stays exactly as it was submitted, and a new draft is
// opened carrying the reason and a copy of the lines to correct. Collapsing this into a
// rejection would turn a correctable mistake into a refusal, which is the difference
// between a member being served and a member giving up.
func (s *Service) Return(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (RequestView, error) {
	if err := domain.ValidateReason(in.ReasonCode, in.ReasonText); err != nil {
		return RequestView{}, err
	}

	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockFor(ctx, tx, rc, id, domain.CommandReturn, in.ExpectedVersion)
		if err != nil {
			return err
		}
		submitted, err := s.repo.GetVersionByNo(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, submitted.ID)
		if err != nil {
			return err
		}
		// The frozen version is marked as replaced and nothing else about it changes; that
		// is the only update migration 000006 lets anybody make to it.
		if err := s.repo.SupersedeVersion(ctx, tx, rc.TenantID, submitted.ID); err != nil {
			return err
		}
		now := s.now().UTC()
		actor := actorPtr(rc.Principal.ActorID)
		nextNo := current.CurrentVersionNo + 1
		draft, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, NewVersionRow{
			ServiceRequestID: id, VersionNo: nextNo,
			ReturnedAt: &now, ReturnedBy: actor,
			ReturnReasonCode: &in.ReasonCode, ReturnReasonText: trimmedPtr(in.ReasonText),
			ActorID: actor,
		})
		if err != nil {
			return err
		}
		// The requester corrects what was sent, not an empty form, so the lines come with
		// it — as fresh REQUESTED lines, because the decisions on the old ones were about
		// the old version.
		if err := s.repo.ReplaceItems(ctx, tx, rc.TenantID, draft.ID, copyItems(items)); err != nil {
			return err
		}
		if err := s.repo.MarkReturned(ctx, tx, rc.TenantID, id, ReturnRow{
			CurrentVersionNo: nextNo, ReasonCode: in.ReasonCode,
			ReviewComment: trimmedPtr(in.ReasonText), ActorID: actor,
		}, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.transition(ctx, tx, rc, id, current.Status, domain.StatusDraft,
			domain.CommandReturn, in.ReasonCode, trimmedPtr(in.ReasonText),
			map[string]any{"from_version_no": current.CurrentVersionNo, "version_no": nextNo}); err != nil {
			return err
		}
		// A return is a decision: somebody looked and answered. The reason itself stays
		// out of the message; the link takes the requester to where it is written.
		if err := s.notifyDecided(ctx, tx, rc, current, statusReturned); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// Reject refuses a request. It is final: the request closes, the version stays frozen, and
// a further attempt is a new request naming this one in supersedesRequestId.
func (s *Service) Reject(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (RequestView, error) {
	if err := domain.ValidateReason(in.ReasonCode, in.ReasonText); err != nil {
		return RequestView{}, err
	}

	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockFor(ctx, tx, rc, id, domain.CommandReject, in.ExpectedVersion)
		if err != nil {
			return err
		}
		version, err := s.repo.GetVersionByNo(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		decisions := make([]ItemDecisionRow, 0, len(items))
		for _, item := range items {
			decisions = append(decisions, ItemDecisionRow{
				LineNo: item.LineNo, Status: domain.ItemRejected,
				DecisionReasonCode: &in.ReasonCode,
			})
		}
		if err := s.repo.DecideItems(ctx, tx, rc.TenantID, version.ID, decisions); err != nil {
			return err
		}
		if err := s.repo.MarkRejected(ctx, tx, rc.TenantID, id, RejectRow{
			ReasonCode: in.ReasonCode, ReviewComment: trimmedPtr(in.ReasonText),
			DecidedAt: s.now().UTC(), ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.transition(ctx, tx, rc, id, current.Status, domain.StatusRejected,
			domain.CommandReject, in.ReasonCode, trimmedPtr(in.ReasonText),
			map[string]any{"version_no": current.CurrentVersionNo}); err != nil {
			return err
		}
		if err := s.notifyDecided(ctx, tx, rc, current, domain.StatusRejected); err != nil {
			return err
		}
		// And anything hanging off this request hears about it, without this package
		// knowing what that is. An inpatient stay (WP-I5-03) is refused here.
		if err := s.publishDecided(ctx, tx, rc, current, domain.StatusRejected); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// Approve approves a request in full unless the decision names lines explicitly.
func (s *Service) Approve(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in DecisionInput,
) (RequestView, error) {
	return s.decide(ctx, rc, id, in, domain.CommandApprove, domain.StatusApproved, false)
}

// PartiallyApprove approves some of what was asked for and reduces or refuses the rest.
func (s *Service) PartiallyApprove(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in DecisionInput,
) (RequestView, error) {
	return s.decide(ctx, rc, id, in, domain.CommandPartiallyApprove, domain.StatusPartiallyApproved, true)
}

// decide is the body both approval commands share. They differ only in the status they
// land on and in how strictly the line decisions are read.
func (s *Service) decide(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in DecisionInput, command, status string, partial bool,
) (RequestView, error) {
	if err := domain.ValidateDecision(in.ReasonCode, in.ReasonText, in.Items, partial); err != nil {
		return RequestView{}, err
	}

	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockFor(ctx, tx, rc, id, command, in.ExpectedVersion)
		if err != nil {
			return err
		}
		version, err := s.repo.GetVersionByNo(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		decisions, err := decisionRows(items, in.Items, in.ReasonCode)
		if err != nil {
			return err
		}
		if err := s.repo.DecideItems(ctx, tx, rc.TenantID, version.ID, decisions); err != nil {
			return err
		}
		if err := s.repo.MarkApproved(ctx, tx, rc.TenantID, id, ApproveRow{
			Status: status, ReviewComment: trimmedPtr(in.ReasonText),
			ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.transition(ctx, tx, rc, id, current.Status, status, command,
			in.ReasonCode, trimmedPtr(in.ReasonText),
			map[string]any{"version_no": current.CurrentVersionNo, "item_count": len(decisions)}); err != nil {
			return err
		}
		if err := s.notifyDecided(ctx, tx, rc, current, status); err != nil {
			return err
		}
		// The same event a rejection publishes, with the status it landed on. An inpatient
		// stay becomes AUTHORIZED off the back of this one, with an authorization taken for
		// the days the reviewer actually approved.
		if err := s.publishDecided(ctx, tx, rc, current, status); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// Cancel withdraws a request that has not been decided. A decided request is not cancelled
// here: undoing a decision releases reservations and may owe a fee, which is the
// cancellation aggregate's business rather than this command's.
func (s *Service) Cancel(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (RequestView, error) {
	if err := domain.ValidateReason(in.ReasonCode, in.ReasonText); err != nil {
		return RequestView{}, err
	}

	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockFor(ctx, tx, rc, id, domain.CommandCancel, in.ExpectedVersion)
		if err != nil {
			return err
		}
		version, err := s.repo.GetVersionByNo(ctx, tx, rc.TenantID, id, current.CurrentVersionNo)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		decisions := make([]ItemDecisionRow, 0, len(items))
		for _, item := range items {
			decisions = append(decisions, ItemDecisionRow{
				LineNo: item.LineNo, Status: domain.ItemCancelled,
				DecisionReasonCode: &in.ReasonCode,
			})
		}
		if err := s.repo.DecideItems(ctx, tx, rc.TenantID, version.ID, decisions); err != nil {
			return err
		}
		if err := s.repo.MarkCancelled(ctx, tx, rc.TenantID, id, CancelRow{
			CancelledAt: s.now().UTC(), ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.transition(ctx, tx, rc, id, current.Status, domain.StatusCancelled,
			domain.CommandCancel, in.ReasonCode, trimmedPtr(in.ReasonText),
			map[string]any{"version_no": current.CurrentVersionNo}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// lockFor takes the request FOR UPDATE and refuses a command that does not apply where the
// request currently is, or that was written against a version the caller no longer holds.
func (s *Service) lockFor(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, command string, expected int64,
) (RequestRecord, error) {
	current, err := s.repo.LockRequest(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return RequestRecord{}, err
	}
	if _, ok := domain.Target(command, current.Status); !ok {
		return RequestRecord{}, ErrTransitionInvalid
	}
	if current.RowVersion != expected {
		return RequestRecord{}, ErrVersionMismatch
	}
	return current, nil
}

// copyItems carries the lines of a returned version into the draft that replaces it. The
// decisions are deliberately not carried: they were about the version that was sent back.
func copyItems(items []ItemRecord) []NewItemRow {
	out := make([]NewItemRow, 0, len(items))
	for i, item := range items {
		row := NewItemRow{
			LineNo: i + 1, ServiceDefinitionID: item.ServiceDefinitionID, UnitType: item.UnitType,
		}
		// The stored text came out of a numeric column, so it parses; a value that somehow
		// did not is carried as zero rather than dropping the line silently.
		if quantity, err := benefitdomain.ParseQuantity(item.RequestedQuantity); err == nil {
			row.RequestedQuantity = quantity
		}
		if item.RequestedAmount != nil {
			if amount, err := benefitdomain.ParseQuantity(*item.RequestedAmount); err == nil {
				row.RequestedAmount = &amount
				row.CurrencyCode = item.CurrencyCode
			}
		}
		out = append(out, row)
	}
	return out
}

// decisionRows turns the reviewer's line decisions into the writes the repository makes.
// A line the reviewer did not mention is approved for what it asked for, which is what
// "approve this request" means; a line naming a number the request never asked for is a
// field error rather than a silent overwrite.
func decisionRows(items []ItemRecord, decided []domain.DecisionItem, reasonCode string) ([]ItemDecisionRow, error) {
	byLine := make(map[int]ItemRecord, len(items))
	for _, item := range items {
		byLine[item.LineNo] = item
	}
	ve := &domain.ValidationError{}
	explicit := make(map[int]ItemDecisionRow, len(decided))
	for i, d := range decided {
		path := fmt.Sprintf("items[%d]", i)
		item, ok := byLine[d.LineNo]
		if !ok {
			ve.Add(path+".lineNo", "NOT_FOUND", "bu satır numarası talepte yok")
			continue
		}
		row := ItemDecisionRow{LineNo: d.LineNo, Status: d.Status}
		if d.DecisionReasonCode != "" {
			code := d.DecisionReasonCode
			row.DecisionReasonCode = &code
		} else {
			code := reasonCode
			row.DecisionReasonCode = &code
		}
		switch d.Status {
		case domain.ItemRejected:
			// A refused line is refused for nothing; carrying an approved quantity here
			// would leave two contradictory statements on the same row.
			zero := benefitdomain.ZeroQuantity()
			row.ApprovedQuantity = &zero
		default:
			quantity, err := approvedQuantity(d.ApprovedQuantity, item.RequestedQuantity)
			if err != nil {
				ve.Add(path+".approvedQuantity", "FORMAT", "kesin ondalık bir sayı olmalı")
				continue
			}
			requested, err := benefitdomain.ParseQuantity(item.RequestedQuantity)
			if err != nil {
				return nil, fmt.Errorf("servicerequest: line %d quantity: %w", d.LineNo, err)
			}
			if quantity.Cmp(requested) > 0 {
				ve.Add(path+".approvedQuantity", "RANGE", "onaylanan miktar istenenden fazla olamaz")
				continue
			}
			row.ApprovedQuantity = &quantity
			amount, ok, err := optionalDecimal(d.ApprovedAmount, item.RequestedAmount)
			if err != nil {
				ve.Add(path+".approvedAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
				continue
			}
			if ok {
				row.ApprovedAmount = &amount
			}
		}
		explicit[d.LineNo] = row
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}

	out := make([]ItemDecisionRow, 0, len(items))
	for _, item := range items {
		if row, ok := explicit[item.LineNo]; ok {
			out = append(out, row)
			continue
		}
		row := ItemDecisionRow{LineNo: item.LineNo, Status: domain.ItemApproved}
		code := reasonCode
		row.DecisionReasonCode = &code
		quantity, err := benefitdomain.ParseQuantity(item.RequestedQuantity)
		if err != nil {
			return nil, fmt.Errorf("servicerequest: line %d quantity: %w", item.LineNo, err)
		}
		row.ApprovedQuantity = &quantity
		if item.RequestedAmount != nil {
			amount, err := benefitdomain.ParseQuantity(*item.RequestedAmount)
			if err != nil {
				return nil, fmt.Errorf("servicerequest: line %d amount: %w", item.LineNo, err)
			}
			row.ApprovedAmount = &amount
		}
		out = append(out, row)
	}
	return out, nil
}

// approvedQuantity reads the reviewer's number, falling back to what was requested.
func approvedQuantity(given, requested string) (benefitdomain.Quantity, error) {
	if given == "" {
		return benefitdomain.ParseQuantity(requested)
	}
	return benefitdomain.ParseQuantity(given)
}

// optionalDecimal reads the reviewer's amount, falling back to the requested one; ok is
// false when neither exists, which leaves the column NULL rather than zero.
func optionalDecimal(given string, requested *string) (benefitdomain.Quantity, bool, error) {
	switch {
	case given != "":
		value, err := benefitdomain.ParseQuantity(given)
		return value, err == nil, err
	case requested != nil:
		value, err := benefitdomain.ParseQuantity(*requested)
		return value, err == nil, err
	default:
		return benefitdomain.ZeroQuantity(), false, nil
	}
}
