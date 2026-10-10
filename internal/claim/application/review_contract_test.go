package application_test

import (
	"context"
	"testing"

	"github.com/celikbros/kapsora/internal/claim/application"
)

func TestManualReviewPreservesSubmittedContractPrice(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "MEDICAL", `serviceCode == "PHYSIO_SESSION"`, actionMedicalReview)
	current := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.physioLine(1, "2", physioTwoContract), f.labLine(2)}, nil))
	for _, stage := range []string{"MEDICAL", "FINANCIAL"} {
		rc := f.medicalRC()
		if stage == "FINANCIAL" {
			rc = f.financialRC()
		}
		next, err := f.claims.DecideLines(context.Background(), rc, current.Claim.ID, application.DecideInput{
			ExpectedVersion: current.Claim.RowVersion,
			Decisions: []application.DecisionInput{
				{LineNo: 1, Decision: "APPROVED", ApprovedQuantity: "2", ApprovedAmount: "400", PayerAmount: "400", MemberAmount: "0", ReasonCode: "REVIEWED"},
				{LineNo: 2, Decision: "APPROVED", ApprovedQuantity: "1", ApprovedAmount: "10", PayerAmount: "10", MemberAmount: "0", ReasonCode: "REVIEWED"},
			},
		}, application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "review"})
		if err != nil {
			t.Fatal(err)
		}
		priced := lineByNo(t, next, 1).Decision
		if priced.ContractAmount == nil || *priced.ContractAmount != physioTwoContract {
			t.Fatalf("%s contract=%v, want submitted %s", stage, priced.ContractAmount, physioTwoContract)
		}
		if lineByNo(t, next, 2).Decision.ContractAmount != nil {
			t.Fatal("unpriced service acquired an invented contract price")
		}
		current = next
	}
	rejected, err := f.claims.Reject(context.Background(), f.financialRC(), current.Claim.ID, application.ReasonInput{ExpectedVersion: current.Claim.RowVersion, ReasonCode: "REFUSED"})
	if err != nil {
		t.Fatal(err)
	}
	priced := lineByNo(t, rejected, 1).Decision
	if priced.ContractAmount == nil || *priced.ContractAmount != physioTwoContract {
		t.Fatal("rejection erased original contract price")
	}
}
