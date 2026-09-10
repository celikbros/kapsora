package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	billingdomain "github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// Dashboard answers the counts and sums an operator reads every morning: claims by status and by
// how long they have waited, icmals awaiting a decision with the oldest SLA, settlements due this
// week and overdue, reimbursements awaiting a decision, and work past its SLA.
//
// **Each figure is one query, and each one carries the filter that reproduces it.** The filter is
// not decoration: a count a person cannot click through to a list of is a number they have to
// take on trust, and the first time it disagrees with the list they will stop trusting the whole
// screen. The filters are built here from the same constants the queries use, so the link and the
// figure cannot drift apart.
//
// There is no percentage on this dashboard and no arithmetic in this function. A ratio is a
// number two screens round differently, and a total assembled from six queries is a total that is
// wrong for the moment between the first and the sixth.
func (s *Service) Dashboard(ctx context.Context, rc identity.RequestContext) (Dashboard, error) {
	scope := scopeOf(rc)
	asOf := s.now().UTC()
	out := Dashboard{AsOf: asOf}
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out.ClaimsByStatus, err = s.repo.ClaimsByStatus(ctx, tx, rc.TenantID, scope); err != nil {
			return err
		}
		if out.ClaimAging, err = s.repo.ClaimAging(ctx, tx, rc.TenantID, scope, asOf); err != nil {
			return err
		}
		if out.Batches, err = s.repo.BatchesAwaitingReview(ctx, tx, rc.TenantID, scope,
			billingdomain.QueueBatchReview); err != nil {
			return err
		}
		if out.Settlements, err = s.repo.SettlementsDue(ctx, tx, rc.TenantID, scope, asOf); err != nil {
			return err
		}
		if out.Reimbursements, err = s.repo.ReimbursementsAwaitingDecision(ctx, tx, rc.TenantID); err != nil {
			return err
		}
		out.WorkItemsPastSLA, err = s.repo.WorkItemsPastSLA(ctx, tx, rc.TenantID, asOf)
		return err
	})
	if err != nil {
		return Dashboard{}, err
	}
	return out, nil
}

// The statuses each dashboard figure counts. They are exported so the transport can hand the
// caller the filter that reproduces the figure, and they are the same lists the queries in
// db/queries/report.sql use — one place, two readers.
var (
	// DashboardOpenClaimStatuses is what the aging figure counts: a claim somebody still has to
	// do something about.
	DashboardOpenClaimStatuses = []string{
		"SUBMITTED", "AUTO_ADJUDICATED", "PENDING_MEDICAL", "PENDING_FINANCIAL", "RETURNED",
	}
	// DashboardBatchStatuses is what the icmal figure counts.
	DashboardBatchStatuses = []string{"SUBMITTED", "UNDER_REVIEW"}
	// DashboardSettlementStatuses is what both settlement figures count. Both arms read the
	// same statuses and differ only in the date, so "due soon" and "overdue" cannot be built
	// from two different ideas of what an open settlement is.
	DashboardSettlementStatuses = []string{"APPROVED", "POSTED", "PARTIALLY_PAID"}
	// DashboardReimbursementStatuses is what the reimbursement figure counts.
	DashboardReimbursementStatuses = []string{"SUBMITTED", "UNDER_REVIEW"}
	// DashboardWorkItemStatuses is what the SLA figure counts.
	DashboardWorkItemStatuses = []string{"OPEN", "CLAIMED", "ESCALATED"}
)

// DashboardDueSoonDays is how far ahead "due this week" looks. It is seven days from the moment
// the dashboard was read, and it is a constant rather than a setting because the link that
// reproduces the figure has to carry the same number.
const DashboardDueSoonDays = 7

// DueSoonUntil is the upper bound of the "due this week" figure, computed once so the figure and
// its filter agree to the day.
func DueSoonUntil(asOf time.Time) time.Time {
	return asOf.UTC().AddDate(0, 0, DashboardDueSoonDays)
}
