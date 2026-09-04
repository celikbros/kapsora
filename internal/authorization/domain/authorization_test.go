package domain_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
)

func TestNewTokenIsUnguessableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		token, err := domain.NewToken()
		if err != nil {
			t.Fatalf("new token: %v", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("token %q is not base64url without padding: %v", token, err)
		}
		if len(raw) != domain.TokenBytes {
			t.Fatalf("token carries %d bytes of entropy, want %d", len(raw), domain.TokenBytes)
		}
		if seen[token] {
			t.Fatalf("token repeated: %s", token)
		}
		seen[token] = true
	}
}

func TestTokenHashIsThirtyTwoBytesAndDeterministic(t *testing.T) {
	first, second := domain.TokenHash("abc"), domain.TokenHash("abc")
	if len(first) != 32 {
		t.Fatalf("digest is %d bytes, want 32", len(first))
	}
	if string(first) != string(second) {
		t.Fatal("the same token must hash to the same digest")
	}
	if string(first) == string(domain.TokenHash("abd")) {
		t.Fatal("different tokens must hash differently")
	}
}

func TestMaskTokenKeepsOnlyTheTail(t *testing.T) {
	token, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	masked := domain.MaskToken(token)
	if strings.Contains(masked, token) {
		t.Fatal("the masked form must not contain the whole token")
	}
	if !strings.HasSuffix(masked, token[len(token)-4:]) {
		t.Fatalf("masked %q does not end with the token's tail", masked)
	}
	if len(masked) >= len(token) {
		t.Fatalf("masked %q is not shorter than the token", masked)
	}
	// A token too short to mask is hidden entirely rather than half-shown.
	if got := domain.MaskToken("ab"); got != "**" {
		t.Fatalf("MaskToken(\"ab\") = %q, want \"**\"", got)
	}
}

func TestNextStatus(t *testing.T) {
	reserved := benefit.MustQuantity("6")
	if got := domain.NextStatus(reserved, benefit.MustQuantity("4")); got != domain.StatusPartiallyUsed {
		t.Fatalf("four of six = %s, want PARTIALLY_USED", got)
	}
	if got := domain.NextStatus(reserved, benefit.MustQuantity("6")); got != domain.StatusUsed {
		t.Fatalf("six of six = %s, want USED", got)
	}
}

func TestValidateExtensionMovesForwardOnly(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	if err := domain.ValidateExtension(now, now.Add(time.Hour), "DELAYED", nil); err != nil {
		t.Fatalf("a later end must be accepted: %v", err)
	}
	if err := domain.ValidateExtension(now, now.Add(-time.Hour), "", nil); err == nil {
		t.Fatal("an earlier end must be refused")
	}
	if err := domain.ValidateExtension(now, now, "", nil); err == nil {
		t.Fatal("the same end must be refused: it extends nothing")
	}
	if err := domain.ValidateExtension(now, now.Add(time.Hour), "lower case", nil); err == nil {
		t.Fatal("a malformed reason code must be refused")
	}
}

func TestValidateNewFulfilmentCatchesDuplicateAndEmptyLines(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	if err := domain.ValidateNewFulfilment(domain.NewFulfilment{PerformedAt: now}); err == nil {
		t.Fatal("a fulfilment with no lines must be refused")
	}
	err := domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: now,
		Items: []domain.FulfilmentItemInput{
			{AuthorizationItemID: "same", ActualQuantity: "1"},
			{AuthorizationItemID: "same", ActualQuantity: "1"},
		},
	})
	if err == nil {
		t.Fatal("the same line twice in one fulfilment must be refused")
	}
	err = domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: now,
		Items:       []domain.FulfilmentItemInput{{AuthorizationItemID: "a", ActualQuantity: "0"}},
	})
	if err == nil {
		t.Fatal("a zero quantity must be refused")
	}
	err = domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: now,
		Items:       []domain.FulfilmentItemInput{{AuthorizationItemID: "a", ActualQuantity: "1.5"}},
	})
	if err != nil {
		t.Fatalf("an exact decimal quantity must be accepted: %v", err)
	}
}

func TestValidateVoucherWindowStaysInsideTheAuthorization(t *testing.T) {
	from := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	if err := domain.ValidateVoucherWindow(from, to, from, to); err != nil {
		t.Fatalf("the authorization's own window must be accepted: %v", err)
	}
	if err := domain.ValidateVoucherWindow(from.Add(-time.Hour), to, from, to); err == nil {
		t.Fatal("a voucher starting before its authorization must be refused")
	}
	if err := domain.ValidateVoucherWindow(from, to.Add(time.Hour), from, to); err == nil {
		t.Fatal("a voucher outliving its authorization must be refused")
	}
}

func TestOpenIsOnlyTheStatesThatStillHold(t *testing.T) {
	for _, status := range []string{domain.StatusActive, domain.StatusPartiallyUsed} {
		if !domain.Open(status) {
			t.Fatalf("%s must still hold entitlement", status)
		}
	}
	for _, status := range []string{domain.StatusUsed, domain.StatusExpired, domain.StatusCancelled} {
		if domain.Open(status) {
			t.Fatalf("%s must not hold entitlement", status)
		}
	}
}
