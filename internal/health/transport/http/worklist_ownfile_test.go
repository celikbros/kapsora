package healthhttp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	workflowhttp "github.com/celikbros/kapsora/internal/workflow/transport/http"
)

func TestReportWorklistClaimRefusesOwnFileAtomically(t *testing.T) {
	s := newServer(t)
	draft := s.createDraft(t, nil)
	s.command(t, draft.Id, "submit", providerReportPermissions, nil)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var itemID uuid.UUID
	var version int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT id, row_version FROM workflow.work_item
		WHERE tenant_id=$1 AND aggregate_type='MEDICAL_REPORT' AND aggregate_id=$2`, s.tenant, draft.Id).Scan(&itemID, &version); err != nil {
		t.Fatal(err)
	}
	rc := reviewerContext(s)
	rc.Permissions["worklist.claim"] = struct{}{}
	rc.SelfPersonID = uuid.NullUUID{UUID: s.person, Valid: true}
	denied := &denyRecorder{}
	handler := workflowhttp.NewHandler(s.workflows, denied, nil)
	router := chi.NewRouter()
	router.Route("/api/v1/work-items", func(r chi.Router) { handler.ItemRoutes(r, workflowhttp.Middlewares{}) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+itemID.String()+"/claim", nil)
	req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, version))
	req = req.WithContext(identity.WithRequestContext(req.Context(), rc))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("own report queue claim = %d, want 403: %s", res.Code, res.Body.String())
	}
	if code := decode[problemBody](t, res).Code; code != "OWN_FILE_DECISION" {
		t.Fatalf("code=%s", code)
	}
	if len(denied.permissions) != 1 || denied.permissions[0] != "worklist.claim" {
		t.Fatal("denial was not audited as a queue claim")
	}
	var status string
	var assignee uuid.NullUUID
	var afterVersion int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT status,assignee_actor_id,row_version FROM workflow.work_item WHERE tenant_id=$1 AND id=$2`, s.tenant, itemID).Scan(&status, &assignee, &afterVersion); err != nil {
		t.Fatal(err)
	}
	if status != "OPEN" || assignee.Valid || afterVersion != version {
		t.Fatal("refusal left a partial work-item claim")
	}
	if s.reportRow(t, draft.Id)["status"] != "SUBMITTED" {
		t.Fatal("own report moved into review")
	}
	var events int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM workflow.status_event WHERE tenant_id=$1 AND aggregate_id=$2`, s.tenant, itemID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatal("refused claim retained a transition event")
	}
	// A different reviewer can still take the untouched item with its original ETag.
	rc.SelfPersonID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	if _, err := s.workflows.ClaimItem(ctx, rc, itemID, version); err != nil {
		t.Fatal(err)
	}
	if s.reportRow(t, draft.Id)["status"] != "UNDER_REVIEW" {
		t.Fatal("another person's review did not start")
	}
}
