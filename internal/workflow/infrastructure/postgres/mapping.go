package workflowpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/workflow/application"
)

// The single-row and the paged reads select the same columns, and sqlc gives each of them
// its own row type; the policies do the same. Rather than several copies of the same
// mapping — which is exactly how a column ends up carried in one read and dropped in
// another — every row is narrowed to one shape here and mapped once.

type queue struct {
	ID                uuid.UUID
	Code              string
	Name              string
	DomainCode        string
	AssignmentPolicy  string
	SlaMinutes        *int32
	EscalationQueueID uuid.NullUUID
	Active            bool
	CreatedAt         time.Time
	RowVersion        int64
}

func queueRow(r sqlcgen.GetWorkQueueRow) queue         { return queue(r) }
func listedQueueRow(r sqlcgen.ListWorkQueuesRow) queue { return queue(r) }

func queueOf(r queue) application.QueueRecord {
	return application.QueueRecord{
		ID: r.ID, Code: r.Code, Name: r.Name, DomainCode: r.DomainCode,
		AssignmentPolicy: r.AssignmentPolicy, SLAMinutes: intPtr(r.SlaMinutes),
		EscalationQueueID: uuidPtr(r.EscalationQueueID), Active: r.Active,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type item struct {
	ID                   uuid.UUID
	QueueID              uuid.UUID
	AggregateType        string
	AggregateID          uuid.UUID
	Title                string
	Priority             int32
	AssigneeActorID      uuid.NullUUID
	AssignedAt           *time.Time
	DueAt                *time.Time
	SlaMinutesSnapshot   *int32
	Status               string
	OutcomeCode          *string
	CompletedAt          *time.Time
	CompletedBy          uuid.NullUUID
	EscalatedAt          *time.Time
	EscalatedFromQueueID uuid.NullUUID
	CreatedAt            time.Time
	RowVersion           int64
	AssigneeDisplayName  *string
}

func itemRow(r sqlcgen.GetWorkItemRow) item         { return item(r) }
func listedItemRow(r sqlcgen.ListWorkItemsRow) item { return item(r) }

func itemOf(r item) application.ItemRecord {
	return application.ItemRecord{
		ID: r.ID, QueueID: r.QueueID, AggregateType: r.AggregateType,
		AggregateID: r.AggregateID, Title: r.Title, Priority: int(r.Priority),
		AssigneeActorID: uuidPtr(r.AssigneeActorID), AssignedAt: r.AssignedAt,
		DueAt: r.DueAt, SLAMinutesSnapshot: intPtr(r.SlaMinutesSnapshot), Status: r.Status,
		OutcomeCode: r.OutcomeCode, CompletedAt: r.CompletedAt,
		CompletedBy: uuidPtr(r.CompletedBy), EscalatedAt: r.EscalatedAt,
		EscalatedFromQueueID: uuidPtr(r.EscalatedFromQueueID),
		CreatedAt:            r.CreatedAt, RowVersion: r.RowVersion,
		AssigneeDisplayName: r.AssigneeDisplayName,
	}
}

type policy struct {
	ID                    uuid.UUID
	ActionCode            string
	ScopeCode             string
	VersionNo             int32
	MinAmount             string
	MaxAmount             string
	RequiredRoleCodes     []string
	RequiredApproverCount int32
	ValidFrom             pgtype.Date
	ValidTo               pgtype.Date
	CreatedAt             time.Time
	RowVersion            int64
}

func listedPolicyRow(r sqlcgen.ListApprovalPoliciesRow) policy    { return policy(r) }
func resolvedPolicyRow(r sqlcgen.ResolveApprovalPolicyRow) policy { return policy(r) }

func policyOf(r policy) application.PolicyRecord {
	return application.PolicyRecord{
		ID: r.ID, ActionCode: r.ActionCode, ScopeCode: r.ScopeCode,
		VersionNo: int(r.VersionNo), MinAmount: trimDecimalPtr(r.MinAmount),
		MaxAmount: trimDecimalPtr(r.MaxAmount), RequiredRoleCodes: roleCodes(r.RequiredRoleCodes),
		RequiredApproverCount: int(r.RequiredApproverCount),
		ValidFrom:             dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func commentOf(r sqlcgen.ListWorkItemCommentsRow) application.CommentRecord {
	return application.CommentRecord{
		ID: r.ID, AggregateType: r.AggregateType, AggregateID: r.AggregateID,
		WorkItemID: uuidPtr(r.WorkItemID), Visibility: r.Visibility, Body: r.Body,
		AuthorActorID: uuidPtr(r.AuthorActorID), CreatedAt: r.CreatedAt,
	}
}

// roleCodes keeps an empty required-role list empty rather than nil, so "no role is
// required" reads the same way in Go as the empty array does in the column.
func roleCodes(codes []string) []string {
	if codes == nil {
		return []string{}
	}
	return codes
}

func intPtr(n *int32) *int {
	if n == nil {
		return nil
	}
	value := int(*n)
	return &value
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return benefitdomain.DateOnly(d.Time)
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	value := benefitdomain.DateOnly(d.Time)
	return &value
}

// trimDecimalPtr turns the empty string the queries render a NULL numeric as back into the
// absence it stands for; anything else keeps its exact value with the trailing zeroes gone.
func trimDecimalPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	value := benefitdomain.TrimDecimal(raw)
	return &value
}
