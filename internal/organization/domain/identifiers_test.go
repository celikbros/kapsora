package domain

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/organization/domain/domaintest"
)

func TestGeneratedTCKNsValidateAndCorruptedOnesDoNot(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 500; i++ {
		v := domaintest.GenerateTCKN(r)
		if err := ValidateTCKN(v); err != nil {
			t.Fatalf("generated TCKN %s rejected: %v", v, err)
		}
		// Corrupt one of the first nine digits: at least one checksum must break.
		pos := r.IntN(9)
		corrupt := []byte(v)
		corrupt[pos] = byte('0' + (int(corrupt[pos]-'0')+1+r.IntN(9))%10)
		if corrupt[0] == '0' {
			corrupt[0] = '5'
		}
		if err := ValidateTCKN(string(corrupt)); err == nil && string(corrupt) != v {
			// A single-digit change can, rarely, keep both checksums valid only if the
			// change is a multiple of 10 in the weighted sum, which one digit cannot be.
			t.Fatalf("corrupted TCKN %s accepted (from %s)", corrupt, v)
		}
	}
	for _, bad := range []string{"", "1234567890", "123456789012", "00000000000", "11111111111", "abcdefghijk", "01234567890"} {
		if err := ValidateTCKN(bad); !errors.Is(err, ErrIdentifierInvalid) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestGeneratedVKNsValidateAndCorruptedOnesDoNot(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 500; i++ {
		v := domaintest.GenerateVKN(r)
		if err := ValidateVKN(v); err != nil {
			t.Fatalf("generated VKN %s rejected: %v", v, err)
		}
		corrupt := []byte(v)
		corrupt[9] = byte('0' + (int(corrupt[9]-'0')+1+r.IntN(9))%10)
		if err := ValidateVKN(string(corrupt)); err == nil {
			t.Fatalf("VKN with a wrong check digit accepted: %s", corrupt)
		}
	}
	for _, bad := range []string{"", "123456789", "12345678901", "abcdefghij"} {
		if err := ValidateVKN(bad); !errors.Is(err, ErrIdentifierInvalid) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestNormalizeValidateAndMask(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	vkn, tckn := domaintest.GenerateVKN(r), domaintest.GenerateTCKN(r)

	if got := Normalize(IdentifierVKN, " "+vkn[:3]+" "+vkn[3:]+" "); got != vkn {
		t.Fatalf("normalize vkn = %q", got)
	}
	if got := Normalize(IdentifierOther, " ab-12 x "); got != "AB12X" {
		t.Fatalf("normalize other = %q", got)
	}
	if err := Validate(IdentifierVKN, vkn); err != nil {
		t.Fatal(err)
	}
	if err := Validate(IdentifierTCKN, tckn); err != nil {
		t.Fatal(err)
	}
	if err := Validate(IdentifierMERSIS, "0123456789012345"); err != nil {
		t.Fatal(err)
	}
	if err := Validate(IdentifierMERSIS, "12345"); !errors.Is(err, ErrIdentifierInvalid) {
		t.Fatalf("short MERSIS: %v", err)
	}
	if err := Validate(IdentifierOther, "REG/2026-001"); err != nil {
		t.Fatal(err)
	}
	if err := Validate(IdentifierOther, "bad value with spaces"); !errors.Is(err, ErrIdentifierInvalid) {
		t.Fatalf("other with spaces: %v", err)
	}
	if err := Validate(IdentifierType("NOPE"), "x"); !errors.Is(err, ErrIdentifierType) {
		t.Fatalf("unknown type: %v", err)
	}

	if got := Mask(IdentifierVKN, vkn); got != vkn[:2]+"******"+vkn[8:] || strings.Contains(got, vkn[2:8]) {
		t.Fatalf("mask vkn = %q", got)
	}
	if got := Mask(IdentifierTCKN, tckn); got != tckn[:3]+"******"+tckn[9:] {
		t.Fatalf("mask tckn = %q", got)
	}
	if got := Mask(IdentifierMERSIS, "0123456789012345"); got != "************2345" {
		t.Fatalf("mask mersis = %q", got)
	}
	if got := Mask(IdentifierOther, "REG2026"); got != "RE*****" {
		t.Fatalf("mask other = %q", got)
	}
	if TaxNumberType(tckn) != IdentifierTCKN || TaxNumberType(vkn) != IdentifierVKN {
		t.Fatal("tax number type inference")
	}
}
