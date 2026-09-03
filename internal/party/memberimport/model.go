package memberimport

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Batch statuses (migration 000018). RECEIVED and VALIDATING are transient, REVIEW and
// READY wait for the operator, APPLYING is owned by the worker job.
const (
	StatusReceived   = "RECEIVED"
	StatusValidating = "VALIDATING"
	StatusReview     = "REVIEW"
	StatusReady      = "READY"
	StatusApplying   = "APPLYING"
	StatusApplied    = "APPLIED"
	StatusFailed     = "FAILED"
	StatusCancelled  = "CANCELLED"
)

// Row statuses (migration 000018).
const (
	RowPending  = "PENDING"
	RowValid    = "VALID"
	RowInvalid  = "INVALID"
	RowMatched  = "MATCHED"
	RowConflict = "CONFLICT"
	RowApplied  = "APPLIED"
	RowSkipped  = "SKIPPED"
)

// Row decisions.
const (
	DecisionCreate = "CREATE"
	DecisionUpdate = "UPDATE"
	DecisionSkip   = "SKIP"
)

// Membership roles carried by the file's membership_type column.
const (
	RolePrincipal = "PRINCIPAL"
	RoleDependant = "DEPENDANT"
)

// BatchStatuses and RowStatuses are the sets the list filters accept.
var (
	BatchStatuses = []string{StatusReceived, StatusValidating, StatusReview, StatusReady,
		StatusApplying, StatusApplied, StatusFailed, StatusCancelled}
	RowStatuses = []string{RowPending, RowValid, RowInvalid, RowMatched, RowConflict, RowApplied, RowSkipped}
	Decisions   = []string{DecisionCreate, DecisionUpdate, DecisionSkip}
)

// Errors mapped by the transport layer to problem+json codes.
var (
	ErrNotFound         = errors.New("memberimport: batch not found")
	ErrRowNotFound      = errors.New("memberimport: row not found")
	ErrDuplicate        = errors.New("memberimport: this file was already uploaded")
	ErrStateInvalid     = errors.New("memberimport: the batch is not in a state that allows this command")
	ErrRowNotReviewable = errors.New("memberimport: the row cannot take this decision")
	ErrVersionMismatch  = errors.New("memberimport: row version does not match If-Match")
)

// Counters is the reconciliation summary of a batch: how the rows of the file ended up.
// valid counts every row that survived validation and matching, invalid and conflict the
// two review buckets, created/updated/skipped the outcome of the apply.
type Counters struct {
	Valid    int
	Invalid  int
	Matched  int
	Conflict int
	Created  int
	Updated  int
	Skipped  int
	// Review is the number of rows waiting for an operator (invalid + conflict).
	Review int
	// PendingApply is the number of accepted rows the apply job has not written yet.
	PendingApply int
}

// Batch is the view returned by upload, get, list, apply and cancel.
type Batch struct {
	ID                    uuid.UUID
	SponsorOrganizationID uuid.UUID
	PlanID                *uuid.UUID
	SourceSystem          string
	SourceVersion         string
	FileName              string
	FileSHA256            string // hex
	Format                string
	RowCount              int
	Status                string
	Counters              Counters
	ErrorSummary          *string
	CreatedAt             time.Time
	AppliedAt             *time.Time
	RowVersion            int64
}

// BatchPage is one keyset page of batches, newest first.
type BatchPage struct {
	Items      []Batch
	NextCursor string
}

// MaskedIdentifier is the only identifier shape that leaves the staging table.
type MaskedIdentifier struct {
	Type        string
	MaskedValue string
}

// RowError is one field-level problem of a staged row.
type RowError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Row is the review-queue view of one staged row.
type Row struct {
	ID                      uuid.UUID
	RowNo                   int
	SourceRecordID          string
	Status                  string
	DisplayName             string
	BirthDate               *time.Time
	MembershipType          *string
	PrincipalSourceRecordID *string
	PlanCode                *string
	Identifiers             []MaskedIdentifier
	MatchedPersonID         *uuid.UUID
	CandidatePersonIDs      []uuid.UUID
	Decision                *string
	Errors                  []RowError
	AppliedPersonID         *uuid.UUID
	RowVersion              int64
}

// RowPage is one page of staged rows in file order.
type RowPage struct {
	Items      []Row
	NextCursor string
}

// rowPayload is the JSON stored in party.import_row.payload. It carries the file's
// non-identifying columns plus what validation resolved. No key of this struct may name
// an identifier column: the CHECK constraint of migration 000018 rejects the row
// otherwise, which is the database's own guarantee that staging holds no plaintext.
type rowPayload struct {
	LineNo         int    `json:"line_no"`
	FirstName      string `json:"first_name"`
	MiddleName     string `json:"middle_name,omitempty"`
	LastName       string `json:"last_name"`
	BirthDate      string `json:"birth_date,omitempty"`
	SexAtBirth     string `json:"sex_at_birth,omitempty"`
	Role           string `json:"membership_role,omitempty"`
	MembershipType string `json:"membership_type,omitempty"`
	Relationship   string `json:"relationship,omitempty"`
	ValidFrom      string `json:"valid_from,omitempty"`
	ValidTo        string `json:"valid_to,omitempty"`
	PlanCode       string `json:"plan_code,omitempty"`

	// PrincipalHash is the blind index of the principal's member number, hex encoded;
	// the plaintext reference of the file never reaches the column.
	PrincipalHash string `json:"principal_hash,omitempty"`
	// PrincipalMasked is what an operator sees instead of the reference itself.
	PrincipalMasked string `json:"principal_masked,omitempty"`
	// PrincipalRecordID is the source_record_id of the principal row of the same file,
	// filled by validation when the reference resolves inside the batch.
	PrincipalRecordID string `json:"principal_source_record_id,omitempty"`
	// PlanID is the plan resolved from plan_code (or the batch default) by validation.
	PlanID string `json:"plan_id,omitempty"`
	// CandidatePersonIDs is filled for CONFLICT rows.
	CandidatePersonIDs []string `json:"candidate_person_ids,omitempty"`
}

// stagedIdentifier is one element of party.import_row.identifiers.
type stagedIdentifier struct {
	Type     string `json:"type"`
	ScopeKey string `json:"scope_key"`
	Hash     string `json:"hash"` // hex encoded blind index
	Masked   string `json:"masked"`
	Primary  bool   `json:"primary,omitempty"`
}

func decodePayload(raw []byte) (rowPayload, error) {
	var p rowPayload
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, nil
}

func decodeIdentifiers(raw []byte) ([]stagedIdentifier, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []stagedIdentifier
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeErrors(raw []byte) ([]RowError, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []RowError
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeJSON(v any) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}
