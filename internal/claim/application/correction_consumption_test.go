package application_test

import (
	"context"
	"testing"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

func TestReturnedClaimResubmissionConsumesTheServiceOnlyOnce(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "CORRECTION_FINANCIAL", `serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)
	authorization := f.authorizeSessions(t, "4")
	draft := f.newClaim(t, []application.NewLineInput{f.physioLine(1, "2", physioTwoContract)}, func(in *application.NewClaimInput) { in.AuthorizationID = &authorization })
	submitted := submitClaim(t, f, draft)
	returned, err := f.claims.Return(context.Background(), f.financialRC(), submitted.Claim.ID, application.ReasonInput{ReasonCode: "AMOUNT_CORRECTION", ExpectedVersion: submitted.Claim.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.claims.PutLines(context.Background(), f.providerRC(), returned.Claim.ID, []application.NewLineInput{f.physioLine(1, "2", "450")}, returned.Claim.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	again := submitClaim(t, f, edited)
	if again.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("corrected status=%s", again.Claim.Status)
	}
	if got := f.consumedTotal(t, authorization); got != "2" {
		t.Fatalf("same two sessions consumed %s after correction", got)
	}
	f.ledgerConserved(t)
}

func TestClaimCorrectionRestoresUsedHoldAndChangesQuantity(t *testing.T) {
	for _, corrected := range []string{"1", "2"} {
		t.Run(corrected, func(t *testing.T) {
			f := newFixture(t)
			f.publishAdjudicationRule(t, "REVIEW", `serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)
			auth := f.authorizeSessions(t, "2")
			submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.physioLine(1, "2", physioTwoContract)}, func(in *application.NewClaimInput) { in.AuthorizationID = &auth }))
			stale := application.ReasonInput{ReasonCode: "CORRECTION", ExpectedVersion: submitted.Claim.RowVersion + 1}
			if _, err := f.claims.Return(context.Background(), f.financialRC(), submitted.Claim.ID, stale); err == nil {
				t.Fatal("stale return accepted")
			}
			if f.consumedTotal(t, auth) != "2" {
				t.Fatal("stale return changed usage")
			}
			for cycle := 0; cycle < 2; cycle++ {
				returned, err := f.claims.Return(context.Background(), f.financialRC(), submitted.Claim.ID, application.ReasonInput{ReasonCode: "CORRECTION", ExpectedVersion: submitted.Claim.RowVersion})
				if err != nil {
					t.Fatal(err)
				}
				if f.consumedTotal(t, auth) != "0" {
					t.Fatal("return did not restore hold")
				}
				old, err := f.claims.GetVersion(context.Background(), f.medicalRC(), submitted.Claim.ID, submitted.Claim.CurrentVersionNo, application.AccessRequest{})
				if err != nil {
					t.Fatal(err)
				}
				if old.Version.Status != domain.VersionSuperseded {
					t.Fatal("history overwritten")
				}
				edited, err := f.claims.PutLines(context.Background(), f.providerRC(), returned.Claim.ID, []application.NewLineInput{f.physioLine(1, corrected, "250")}, returned.Claim.RowVersion)
				if err != nil {
					t.Fatal(err)
				}
				submitted = submitClaim(t, f, edited)
				if f.consumedTotal(t, auth) != corrected {
					t.Fatal("corrected quantity was not applied exactly")
				}
			}
			f.ledgerConserved(t)
		})
	}
}

func TestClaimCorrectionOverconsumptionHasNothingToReverse(t *testing.T) {
	f := newFixture(t)
	auth := f.authorizeSessions(t, "2")
	submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.physioLine(1, "3", "750")}, func(in *application.NewClaimInput) { in.AuthorizationID = &auth }))
	if f.consumedTotal(t, auth) != "0" {
		t.Fatal("overconsumption moved balance")
	}
	returned, err := f.claims.Return(context.Background(), f.medicalRC(), submitted.Claim.ID, application.ReasonInput{ReasonCode: "QUANTITY_CORRECTION", ExpectedVersion: submitted.Claim.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.claims.PutLines(context.Background(), f.providerRC(), returned.Claim.ID, []application.NewLineInput{f.physioLine(1, "2", physioTwoContract)}, returned.Claim.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	submitClaim(t, f, edited)
	if f.consumedTotal(t, auth) != "2" {
		t.Fatal("corrected quantity not consumed")
	}
	f.ledgerConserved(t)
}
