package reporthttp

import (
	"net/http"
	"time"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/report/application"
)

// GetOperationsDashboard answers GET /operations/dashboard.
func (h *Handler) GetOperationsDashboard(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	dashboard, err := h.svc.Dashboard(r.Context(), rc)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dashboardView(dashboard))
}

// dashboardView renders every figure with the filter that reproduces it.
//
// The filters are built here from the same constants the queries use, so the link a person clicks
// and the number they clicked it from cannot drift apart. That is the whole reason they are on the
// wire at all: a count nobody can check is a count nobody will believe twice.
func dashboardView(d application.Dashboard) kapsorav1.OperationsDashboard {
	statuses := make([]kapsorav1.DashboardClaimStatusFigure, 0, len(d.ClaimsByStatus))
	for _, figure := range d.ClaimsByStatus {
		statuses = append(statuses, kapsorav1.DashboardClaimStatusFigure{
			Status: figure.Status, ClaimCount: int(figure.ClaimCount),
			ApprovedTotal: figure.ApprovedTotal,
			Filter: kapsorav1.DashboardFilter{
				Resource: "claims", Statuses: &[]string{figure.Status},
			},
		})
	}
	aging := make([]kapsorav1.DashboardAgingFigure, 0, len(d.ClaimAging))
	for _, figure := range d.ClaimAging {
		bucket := kapsorav1.DashboardAgingFigureBucket(figure.Bucket)
		filterBucket := kapsorav1.DashboardFilterAgingBucket(figure.Bucket)
		aging = append(aging, kapsorav1.DashboardAgingFigure{
			Bucket: bucket, ClaimCount: int(figure.ClaimCount),
			ApprovedTotal: figure.ApprovedTotal,
			Filter: kapsorav1.DashboardFilter{
				Resource:    "claims",
				Statuses:    copyOf(application.DashboardOpenClaimStatuses),
				AgingBucket: &filterBucket,
			},
		})
	}
	dueSoonUntil := application.DueSoonUntil(d.AsOf)
	return kapsorav1.OperationsDashboard{
		AsOf:           d.AsOf,
		ClaimsByStatus: statuses,
		ClaimAging:     aging,
		BatchesAwaitingReview: kapsorav1.DashboardBatchFigure{
			BatchCount: int(d.Batches.BatchCount), SubmittedTotal: d.Batches.SubmittedTotal,
			OldestSubmittedAt: d.Batches.OldestSubmittedAt,
			OldestSlaDueAt:    d.Batches.OldestSLADueAt,
			Filter: kapsorav1.DashboardFilter{
				Resource: "batches", Statuses: copyOf(application.DashboardBatchStatuses),
			},
		},
		Settlements: kapsorav1.DashboardSettlementFigure{
			DueSoonCount: int(d.Settlements.DueSoonCount),
			DueSoonTotal: d.Settlements.DueSoonTotal,
			OverdueCount: int(d.Settlements.OverdueCount),
			OverdueTotal: d.Settlements.OverdueTotal,
			DueSoonFilter: kapsorav1.DashboardFilter{
				Resource: "settlements",
				Statuses: copyOf(application.DashboardSettlementStatuses),
				DueFrom:  optDateOf(&d.AsOf), DueTo: optDateOf(&dueSoonUntil),
			},
			OverdueFilter: kapsorav1.DashboardFilter{
				Resource:  "settlements",
				Statuses:  copyOf(application.DashboardSettlementStatuses),
				DueBefore: optDateOf(&d.AsOf),
			},
		},
		Reimbursements: kapsorav1.DashboardReimbursementFigure{
			ReimbursementCount: int(d.Reimbursements.ReimbursementCount),
			RequestedTotal:     d.Reimbursements.RequestedTotal,
			OldestSubmittedAt:  d.Reimbursements.OldestSubmittedAt,
			Filter: kapsorav1.DashboardFilter{
				Resource: "reimbursements",
				Statuses: copyOf(application.DashboardReimbursementStatuses),
			},
		},
		WorkItemsPastSla: kapsorav1.DashboardWorkItemFigure{
			ItemCount: int(d.WorkItemsPastSLA.ItemCount), OldestDueAt: d.WorkItemsPastSLA.OldestDueAt,
			Filter: kapsorav1.DashboardFilter{
				Resource: "workItems", Statuses: copyOf(application.DashboardWorkItemStatuses),
				OverdueAt: overdueAt(d.AsOf),
			},
		},
	}
}

// copyOf hands the filter its own slice. The application's lists are package-level values, and a
// response that pointed at one would let a caller of this function mutate the constant.
func copyOf(list []string) *[]string {
	out := append([]string(nil), list...)
	return &out
}

func overdueAt(at time.Time) *time.Time {
	value := at
	return &value
}
