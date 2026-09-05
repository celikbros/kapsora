package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// ReportExpireBatchSize is how many reports one pass of the expiry job takes per tenant.
const ReportExpireBatchSize = 200

// SubmitReport hands a draft to the medical reviewers.
//
// Two things stop it, and both are refusals rather than something the submit fixes on the
// way past. A report with no service line is a reviewer asked to approve nothing in
// particular. A report with no scanned-clean document is a reviewer asked to decide on a
// file nobody can open — a link to an object still in quarantine is not a document, and one
// the scanner called infected is a document that no longer has any bytes.
//
// The work item is raised in this same transaction, so a report that was submitted with
// nobody told about it, or an item raised for a submission that rolled back, is a state that
// never exists.
func (s *Service) SubmitReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (ReportView, error) {
	now := s.now().UTC()

	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockForCommand(ctx, tx, rc, id, domain.ReportCommandSubmit, expected)
		if err != nil {
			return err
		}
		lines, err := s.reports.CountReportServices(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if lines == 0 {
			return ErrReportServiceRequired
		}
		documents, err := s.reports.CountCleanReportDocuments(ctx, tx, rc.TenantID, id, current.ReportType)
		if err != nil {
			return err
		}
		if documents == 0 {
			return ErrReportDocumentRequired
		}
		if err := s.reports.MarkReportSubmitted(ctx, tx, rc.TenantID, id, now,
			actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		// The reference and nothing else. A queue is a list people read across a room, and
		// a title carrying the report type would put a diagnosis on a wall.
		if err := s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
			QueueCode: MedicalReviewQueueCode, AggregateType: domain.AggregateMedicalReport,
			AggregateID: id, Title: "Tıbbi rapor " + current.Reference,
			ActorID: actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return err
		}
		if err := s.recordReport(ctx, tx, rc, "medical_report.submit", current, map[string]any{
			"lines": lines, "documents": documents,
		}); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// StartReview moves a submitted report to UNDER_REVIEW: a reviewer has picked it up. The
// worklist raises the same transition when the work item is claimed, so a reviewer who works
// from the queue never has to give this command at all.
func (s *Service) StartReview(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (ReportView, error) {
	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockForCommand(ctx, tx, rc, id, domain.ReportCommandStartReview, expected)
		if err != nil {
			return err
		}
		moved, err := s.reports.MarkReportUnderReview(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		if !moved {
			// The status predicate is the whole precondition and the row is locked, so
			// nothing could have moved it in between. Reaching here means the lifecycle
			// check above and the statement disagree, which is worth saying out loud.
			return ErrReportTransitionInvalid
		}
		if err := s.recordReport(ctx, tx, rc, "medical_report.start_review", current, nil); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// ApproveReport is the decision claims and authorizations lean on.
//
// If this is a correction, the version it corrects leaves APPROVED in the same transaction,
// so the chain has exactly one approved version at every moment a reader could observe —
// never two, and never none. Nothing else about the older version moves: the reviewer, the
// moment, the comment, the summary, the lines and the usage rows stay exactly as they were
// decided, because "what did the reviewer approve on the fifth" has to stay answerable.
func (s *Service) ApproveReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	comment *string, expected int64,
) (ReportView, error) {
	return s.decideReport(ctx, rc, id, domain.ReportCommandApprove, comment, "", expected)
}

// RejectReport is the other half of the same decision, and it says why in a code: a rejection
// nobody can count is a rejection nobody can improve on.
func (s *Service) RejectReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	comment *string, reasonCode string, expected int64,
) (ReportView, error) {
	return s.decideReport(ctx, rc, id, domain.ReportCommandReject, comment, reasonCode, expected)
}

// decideReport is both decisions, because they are one act with two answers: the same
// preconditions, the same audit shape, and the same notification. Writing them twice would
// be two places for the freeze or the notification to be forgotten in.
func (s *Service) decideReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	command string, comment *string, reasonCode string, expected int64,
) (ReportView, error) {
	reject := command == domain.ReportCommandReject
	if err := domain.ValidateReportDecision(comment, reasonCode, reject); err != nil {
		return ReportView{}, err
	}
	now := s.now().UTC()

	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockForCommand(ctx, tx, rc, id, command, expected)
		if err != nil {
			return err
		}
		decision := ReportDecisionRow{
			ReviewComment: trimmedPtr(comment), RejectReasonCode: reasonCode,
			ReviewedAt: now, ReviewedBy: rc.Principal.ActorID, Expected: expected,
		}
		if decision.ReviewedBy == uuid.Nil {
			return fmt.Errorf("health: deciding a report needs an actor")
		}

		superseded := 0
		if !reject {
			// The approval moves to this version. It happens before the write below so
			// the partial unique index never sees two approved rows, even for an instant.
			superseded, err = s.supersedeChain(ctx, tx, rc, current)
			if err != nil {
				return err
			}
			err = s.reports.MarkReportApproved(ctx, tx, rc.TenantID, id, decision)
		} else {
			err = s.reports.MarkReportRejected(ctx, tx, rc.TenantID, id, decision)
		}
		if err != nil {
			return err
		}

		status := domain.ReportStatusApproved
		if reject {
			status = domain.ReportStatusRejected
		}
		detail := map[string]any{"decision": status, "superseded": superseded}
		if reject {
			// The reason code, never the comment: the comment is clinical text and an
			// audit detail is read by everybody who may read audit.
			detail["reason_code"] = reasonCode
		}
		if err := s.recordReport(ctx, tx, rc, "medical_report.decide", current, detail); err != nil {
			return err
		}
		if err := s.notifyReportDecided(ctx, tx, rc, current, status, now); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// supersedeChain moves every other approved version of this chain out of APPROVED and reports
// how many it moved. In an ordinary chain that is one or none; the loop rather than a single
// lookup is because "at most one" is an invariant of the index, and code that assumed it
// would be code that silently leaves a second one behind if the index ever changed.
func (s *Service) supersedeChain(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	current ReportRecord,
) (int, error) {
	chain, err := s.reports.ListReportChain(ctx, tx, rc.TenantID, current.RootReportID)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, version := range chain {
		if version.ID == current.ID || version.Status != domain.ReportStatusApproved {
			continue
		}
		ok, err := s.reports.SupersedeReport(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return 0, err
		}
		if ok {
			moved++
		}
	}
	return moved, nil
}

// CancelReport takes back a report nobody has started reading. Once a reviewer has picked it
// up it is theirs to decide, and a withdrawal at that point would be the provider deciding
// instead.
func (s *Service) CancelReport(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (ReportView, error) {
	var out ReportView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockForCommand(ctx, tx, rc, id, domain.ReportCommandCancel, expected)
		if err != nil {
			return err
		}
		if err := s.reports.MarkReportCancelled(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		if err := s.recordReport(ctx, tx, rc, "medical_report.cancel", current, nil); err != nil {
			return err
		}
		out, err = s.reloadReport(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ReportView{}, err
	}
	return out, nil
}

// lockForCommand is the preamble every lifecycle command shares: read the row under FOR
// UPDATE so two commands on one report serialise, check the lifecycle allows the command
// from the status the report is actually in, and check the caller is acting on the version
// it read.
//
// The order matters. A frozen report answers ErrReportImmutable rather than a transition
// error, because "you cannot edit an approved report" is what the caller needs to hear and
// "that command is not allowed here" is not.
func (s *Service) lockForCommand(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, command string, expected int64,
) (ReportRecord, error) {
	current, err := s.reports.LockReport(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return ReportRecord{}, err
	}
	if _, ok := domain.ReportTarget(command, current.Status); !ok {
		if domain.ReportDecided(current.Status) {
			return ReportRecord{}, ErrReportImmutable
		}
		return ReportRecord{}, ErrReportTransitionInvalid
	}
	if current.RowVersion != expected {
		return ReportRecord{}, ErrVersionMismatch
	}
	return current, nil
}

// recordReport writes one business audit row about a report. Every detail below is an id, a
// code, a count or a status, and never the report type, the subtype, the summary or the
// review comment: an audit detail is read by everybody who may read audit, and this one is
// about a person's health. audit.SanitizeDetail would drop a badly named key silently, so
// nothing here is named in a way that would make it disappear.
func (s *Service) recordReport(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, record ReportRecord, extra map[string]any,
) error {
	detail := map[string]any{
		"reference":  record.Reference,
		"version_no": record.VersionNo,
		"person":     record.PersonID,
		"root":       record.RootReportID,
	}
	for k, v := range extra {
		detail[k] = v
	}
	return s.record(ctx, tx, rc, action, domain.AggregateMedicalReport, record.ID, detail)
}

// notifyReportDecided tells the member and the issuing provider that a reviewer has answered.
//
// It runs inside the command's transaction and writes outbox rows and nothing else: a
// decision that rolls back has notified nobody, and a notification can neither slow a
// decision down nor make one fail. What it carries is a reference, a status word, a day and
// a link. There is no slot in the template for a review comment, a report type or a
// diagnosis, and the point of only ever passing these four is that the renderer never has to
// refuse one.
func (s *Service) notifyReportDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ReportRecord, statusCode string, now time.Time,
) error {
	recipients := []notificationapp.Recipient{notificationapp.PersonRecipient(record.PersonID)}
	if record.IssuingProviderOrganizationID != nil {
		recipients = append(recipients,
			notificationapp.OrganizationRecipient(*record.IssuingProviderOrganizationID))
	}
	return notificationapp.PublishNotification(ctx, tx, rc.TenantID, notificationapp.Notification{
		EventCode: notificationapp.EventMedicalReportDecided,
		// The report version and the status it landed on: replaying the same decision
		// produces the same key, and the message table refuses the second copy.
		Key:        record.ID.String() + ":" + statusCode,
		Recipients: recipients,
		Variables: map[string]string{
			notificationdomain.VarReferenceNo: record.Reference,
			notificationdomain.VarStatusCode:  statusCode,
			notificationdomain.VarEventDate:   now.UTC().Format(time.DateOnly),
			notificationdomain.VarDeepLink:    notificationapp.DeepLink("medical-reports", record.ID),
		},
	})
}

// WorkItemClaimed is the worklist's hook: a reviewer taking the work item of a submitted
// report starts the review, so a reviewer who works from the queue never has to give the
// command separately. It runs inside the claim's own transaction, so the item and the report
// move together or not at all.
//
// It is deliberately quiet about everything it is not for. An item of another aggregate type
// is not this module's business; a claimer who does not hold the review grant took the item
// for some other reason and is not thereby a reviewer; and a report that is not SUBMITTED —
// one already under review, or decided while the item sat in the queue — is left where it
// is, because a claim is not a decision.
func (s *Service) WorkItemClaimed(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	aggregateType string, aggregateID uuid.UUID,
) error {
	if aggregateType != domain.AggregateMedicalReport || !rc.Has(PermissionReportReview) {
		return nil
	}
	moved, err := s.reports.MarkReportUnderReview(ctx, tx, rc.TenantID, aggregateID,
		actorPtr(rc.Principal.ActorID))
	if err != nil {
		return err
	}
	if !moved {
		return nil
	}
	return s.record(ctx, tx, rc, "medical_report.start_review", domain.AggregateMedicalReport,
		aggregateID, map[string]any{"source": "WORK_ITEM_CLAIM"})
}

// ExpireReports marks every approved report past its validity EXPIRED, tenant by tenant and
// in batches, until nothing is left. It is the body of the medical_report.expire scheduler
// job.
//
// Running it twice expires once, and that is a property of the two statements rather than of
// a flag somebody has to remember to check: the listing only looks at APPROVED rows, and the
// update names APPROVED in its own predicate. A second pass therefore lists nothing the
// first one finished, and a row another pass took between the listing and the write is
// skipped rather than written twice.
func (s *Service) ExpireReports(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, tenantID := range tenants {
		for {
			// The loop continues on how many rows were taken, not on how many were
			// expired: counting only the writes would end the sweep early with work
			// still waiting.
			listed, count, err := s.expireReportBatch(ctx, tenantID, now)
			if err != nil {
				return expired, err
			}
			expired += count
			if listed < ReportExpireBatchSize {
				break
			}
		}
	}
	return expired, nil
}

func (s *Service) expireReportBatch(ctx context.Context, tenantID uuid.UUID, now time.Time) (listed, count int, err error) {
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			ids, err := s.reports.ListExpirableReports(ctx, tx, tenantID, now, ReportExpireBatchSize)
			if err != nil {
				return err
			}
			listed = len(ids)
			for _, id := range ids {
				marked, err := s.reports.MarkReportExpired(ctx, tx, tenantID, id)
				if err != nil {
					return err
				}
				if !marked {
					// Another pass finished this row between the listing and here. It is
					// not an error: the report is expired either way.
					continue
				}
				count++
			}
			return nil
		})
	if err != nil {
		return 0, 0, fmt.Errorf("health: expire reports of tenant %s: %w", tenantID, err)
	}
	return listed, count, nil
}

// activeTenants lists the tenants the expiry job walks, outside any tenant transaction.
func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	conn, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("health: begin tenant listing: %w", err)
	}
	defer func() { _ = conn.Rollback(ctx) }()
	return s.reports.ActiveTenants(ctx, conn)
}
