package servicerequesthttp

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// SubmitServiceRequest implements submitServiceRequest.
func (h *Handler) SubmitServiceRequest(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSubmit)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewComment
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	view, err := h.svc.Submit(r.Context(), rc, id, body.Comment, expected)
	h.answer(w, r, view, err)
}

// ReturnServiceRequest implements returnServiceRequest.
func (h *Handler) ReturnServiceRequest(w http.ResponseWriter, r *http.Request) {
	h.reasonCommand(w, r, PermissionReview, func(rc identity.RequestContext, id uuid.UUID,
		in application.ReasonInput,
	) (application.RequestView, error) {
		return h.svc.Return(r.Context(), rc, id, in)
	})
}

// RejectServiceRequest implements rejectServiceRequest.
func (h *Handler) RejectServiceRequest(w http.ResponseWriter, r *http.Request) {
	h.reasonCommand(w, r, PermissionReview, func(rc identity.RequestContext, id uuid.UUID,
		in application.ReasonInput,
	) (application.RequestView, error) {
		return h.svc.Reject(r.Context(), rc, id, in)
	})
}

// CancelServiceRequest implements cancelServiceRequest.
func (h *Handler) CancelServiceRequest(w http.ResponseWriter, r *http.Request) {
	h.reasonCommand(w, r, PermissionCancel, func(rc identity.RequestContext, id uuid.UUID,
		in application.ReasonInput,
	) (application.RequestView, error) {
		return h.svc.Cancel(r.Context(), rc, id, in)
	})
}

// ApproveServiceRequest implements approveServiceRequest.
func (h *Handler) ApproveServiceRequest(w http.ResponseWriter, r *http.Request) {
	h.decisionCommand(w, r, func(rc identity.RequestContext, id uuid.UUID,
		in application.DecisionInput,
	) (application.RequestView, error) {
		return h.svc.Approve(r.Context(), rc, id, in)
	})
}

// PartiallyApproveServiceRequest implements partiallyApproveServiceRequest.
func (h *Handler) PartiallyApproveServiceRequest(w http.ResponseWriter, r *http.Request) {
	h.decisionCommand(w, r, func(rc identity.RequestContext, id uuid.UUID,
		in application.DecisionInput,
	) (application.RequestView, error) {
		return h.svc.PartiallyApprove(r.Context(), rc, id, in)
	})
}

// reasonCommand is the shape return, reject and cancel share: an id, an If-Match and a
// reason. They are separate routes with separate permissions and this only spares the
// plumbing, never the distinction between them.
func (h *Handler) reasonCommand(w http.ResponseWriter, r *http.Request, permission string,
	run func(identity.RequestContext, uuid.UUID, application.ReasonInput) (application.RequestView, error),
) {
	rc, ok := h.require(w, r, permission)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReasonCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := run(rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	})
	h.answer(w, r, view, err)
}

// decisionCommand is the shape approve and partiallyApprove share.
func (h *Handler) decisionCommand(w http.ResponseWriter, r *http.Request,
	run func(identity.RequestContext, uuid.UUID, application.DecisionInput) (application.RequestView, error),
) {
	rc, ok := h.require(w, r, PermissionReview)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ServiceRequestDecision
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.DecisionInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	}
	if body.Items != nil {
		in.Items = decisionItems(*body.Items)
	}
	view, err := run(rc, id, in)
	h.answer(w, r, view, err)
}

// answer writes the request with its new ETag, or the problem the command produced.
func (h *Handler) answer(w http.ResponseWriter, r *http.Request, view application.RequestView, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Request.RowVersion))
	writeJSON(w, http.StatusOK, requestView(view))
}

func decisionItems(in []kapsorav1.ServiceRequestDecisionItem) []domain.DecisionItem {
	out := make([]domain.DecisionItem, 0, len(in))
	for _, item := range in {
		row := domain.DecisionItem{LineNo: item.LineNo, Status: string(item.Status)}
		if item.ApprovedQuantity != nil {
			row.ApprovedQuantity = *item.ApprovedQuantity
		}
		if item.ApprovedAmount != nil {
			row.ApprovedAmount = *item.ApprovedAmount
		}
		if item.DecisionReasonCode != nil {
			row.DecisionReasonCode = *item.DecisionReasonCode
		}
		out = append(out, row)
	}
	return out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := t.UTC()
	return &value
}
