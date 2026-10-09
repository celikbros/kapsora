package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
)

type enrollmentCheckStub struct {
	result eligibility.ResultView
	input  eligibility.CheckInput
}

func (s *enrollmentCheckStub) Check(_ context.Context, _ identity.RequestContext,
	in eligibility.CheckInput,
) (eligibility.ResultView, error) {
	s.input = in
	return s.result, nil
}

func TestPinnedHoldRejectsDifferentEligibilityEnrollment(t *testing.T) {
	selected, other, program, definition := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name string
		id   *uuid.UUID
	}{
		{"missing", nil},
		{"different", &other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &enrollmentCheckStub{result: eligibility.ResultView{
				EnrollmentID: tc.id, EvaluationID: uuid.New(),
			}}
			svc := &Service{eligibility: checker}
			_, err := svc.checkEligibility(context.Background(), identity.RequestContext{},
				SearchInput{PersonID: uuid.New(), ProgramID: &program}, day("2026-06-15"),
				searchWorld{roomTypes: []AvailabilityRoomType{{ServiceDefinitionID: definition}}}, &selected)
			if !errors.Is(err, ErrEnrollmentNotFound) {
				t.Fatalf("mismatched evaluation = %v, want enrollment refusal", err)
			}
			if checker.input.EnrollmentID == nil || *checker.input.EnrollmentID != selected ||
				checker.input.ProgramID == nil || *checker.input.ProgramID != program {
				t.Fatalf("eligibility question lost resolved plan: %+v", checker.input)
			}
		})
	}
}
