package healthhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
)

// CreateEncounter implements createEncounter.
//
// It needs health.clinical.read beside health.case.manage. The body may carry a branch code
// and clinical notes, and a caller that may not read those has no business writing them:
// it could not read back what it wrote, and the answer would be the financial projection of
// something it had just typed.
func (h *Handler) CreateEncounter(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	if _, ok := h.require(w, r, PermissionClinicalRead); !ok {
		return
	}
	caseID, ok := h.pathUUID(w, r, "caseId", application.ErrCaseNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateEncounter
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CreateEncounter(r.Context(), rc, application.NewEncounterInput{
		CaseID: caseID, EncounterType: string(body.EncounterType), StartedAt: body.StartedAt,
		EndedAt: body.EndedAt, LocationID: body.LocationId, PractitionerID: body.PractitionerId,
		BranchCode: body.BranchCode, NotesClinical: body.NotesClinical,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Encounter.RowVersion))
	w.Header().Set("Location", "/api/v1/encounters/"+view.Encounter.ID.String())
	writeJSON(w, http.StatusCreated, encounterBody(view.Encounter, view.Projection))
}

// GetEncounter implements getEncounter.
func (h *Handler) GetEncounter(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "encounterId", application.ErrEncounterNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetEncounter(r.Context(), rc, id, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Encounter.RowVersion))
	writeJSON(w, http.StatusOK, encounterBody(view.Encounter, view.Projection))
}

// ListEncounterDiagnoses implements listEncounterDiagnoses.
func (h *Handler) ListEncounterDiagnoses(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "encounterId", application.ErrEncounterNotFound)
	if !ok {
		return
	}
	rows, err := h.svc.ListDiagnoses(r.Context(), rc, id, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, diagnosisList(rows))
}

// PutEncounterDiagnoses implements putEncounterDiagnoses.
func (h *Handler) PutEncounterDiagnoses(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	// Writing a diagnosis needs the grant to read one. The answer this endpoint gives is
	// the stored set, and there is no financial projection of a diagnosis to give instead.
	if _, ok := h.require(w, r, PermissionClinicalRead); !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "encounterId", application.ErrEncounterNotFound)
	if !ok {
		return
	}
	var body kapsorav1.PutEncounterDiagnoses
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.DiagnosisInput, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, domain.DiagnosisInput{
			CodeValueID: item.CodeValueId.String(), DiagnosisType: string(item.DiagnosisType),
		})
	}
	rows, err := h.svc.PutDiagnoses(r.Context(), rc, id, items)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, diagnosisList(rows))
}

// ListHealthAccessLog implements listHealthAccessLog.
func (h *Handler) ListHealthAccessLog(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionAuditRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	personID := queryUUID(r, "personId", &fields)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListAccessLog(r.Context(), rc, personID, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.HealthAccessLogPage{
		Items: make([]kapsorav1.HealthAccessEvent, 0, len(page.Items)),
	}
	for _, row := range page.Items {
		event := kapsorav1.HealthAccessEvent{
			Id: row.ID, OccurredAt: row.OccurredAt, ActorId: row.ActorID,
			ResourceType: row.ResourceType,
			AccessType:   kapsorav1.HealthAccessEventAccessType(row.AccessType),
			Outcome:      kapsorav1.HealthAccessEventOutcome(row.Outcome),
			PurposeCode:  row.PurposeCode, ReasonText: row.ReasonText,
		}
		if row.MembershipID != nil {
			id := *row.MembershipID
			event.MembershipId = &id
		}
		if row.PersonID != nil {
			id := *row.PersonID
			event.PersonId = &id
		}
		if row.ResourceID != nil {
			id := *row.ResourceID
			event.ResourceId = &id
		}
		out.Items = append(out.Items, event)
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// encounterBody maps a projected encounter onto the wire. Like caseBody it has no rule of
// its own: branchCode and notesClinical are nil here whenever the service cleared them.
func encounterBody(record application.EncounterRecord, projection application.Projection) kapsorav1.Encounter {
	out := kapsorav1.Encounter{
		Id:            record.ID,
		CaseId:        record.CaseID,
		EncounterType: kapsorav1.EncounterType(record.EncounterType),
		StartedAt:     record.StartedAt,
		EndedAt:       record.EndedAt,
		Projection:    kapsorav1.HealthProjection(projection),
		BranchCode:    record.BranchCode,
		NotesClinical: record.NotesClinical,
		CreatedAt:     record.CreatedAt,
		RowVersion:    record.RowVersion,
	}
	if record.LocationID != nil {
		id := *record.LocationID
		out.LocationId = &id
	}
	if record.PractitionerID != nil {
		id := *record.PractitionerID
		out.PractitionerId = &id
	}
	return out
}

func diagnosisList(rows []application.DiagnosisRecord) kapsorav1.DiagnosisList {
	out := kapsorav1.DiagnosisList{Items: make([]kapsorav1.Diagnosis, 0, len(rows))}
	for _, row := range rows {
		item := kapsorav1.Diagnosis{
			Id: row.ID, EncounterId: row.EncounterID, CodeSystemId: row.CodeSystemID,
			CodeSystemCode: row.CodeSystemCode, CodeValueId: row.CodeValueID,
			Code: row.Code, Display: row.Display,
			DiagnosisType: kapsorav1.DiagnosisType(row.DiagnosisType),
			Sensitive:     row.Sensitive, RecordedAt: row.RecordedAt,
		}
		if row.RecordedBy != nil {
			id := *row.RecordedBy
			item.RecordedBy = &id
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// EndEncounter implements endEncounter; no clinical content is edited by this command.
func (h *Handler) EndEncounter(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	if _, ok := h.require(w, r, PermissionClinicalRead); !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "encounterId", application.ErrEncounterNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.EndEncounter
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.EndEncounter(r.Context(), rc, id, body.EndedAt, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Encounter.RowVersion))
	writeJSON(w, http.StatusOK, encounterBody(view.Encounter, view.Projection))
}
