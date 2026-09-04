package scheduler

import (
	"context"
	"time"

	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
)

// WorkflowEscalate moves overdue work items to their queue's escalation target, or marks
// them ESCALATED where the queue names none (WP-I4-03 section 2.3). The sweep only looks
// at items that have not been escalated yet and every write repeats that predicate, so a
// second run — or a run after a crash halfway through — escalates nothing twice. It never
// moves a due date: a late item stays late.
func WorkflowEscalate(svc *workflowapp.Service) Job {
	return Job{
		Code:  "workflow.escalate",
		Every: time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			escalated, err := svc.EscalateOverdue(ctx, time.Now().UTC())
			return Metrics{"escalated": escalated}, err
		},
	}
}
