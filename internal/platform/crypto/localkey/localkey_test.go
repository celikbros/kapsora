package localkey

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/crypto"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	key, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	tenant := uuid.New()

	env, err := p.Encrypt(ctx, tenant, crypto.PurposePersonIdentifier, []byte("12345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte("12345678901")) {
		t.Fatal("plaintext visible in envelope")
	}
	got, err := p.Decrypt(ctx, tenant, crypto.PurposePersonIdentifier, env)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "12345678901" {
		t.Fatalf("round trip = %q", got)
	}

	// Two encryptions of the same value differ (random nonce).
	env2, _ := p.Encrypt(ctx, tenant, crypto.PurposePersonIdentifier, []byte("12345678901"))
	if bytes.Equal(env, env2) {
		t.Fatal("envelopes must not be deterministic")
	}
}

func TestDecryptRejectsTamperingWrongTenantAndWrongPurpose(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	tenant := uuid.New()
	env, err := p.Encrypt(ctx, tenant, crypto.PurposePersonIdentifier, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	tampered := append([]byte(nil), env...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := p.Decrypt(ctx, tenant, crypto.PurposePersonIdentifier, tampered); !errors.Is(err, crypto.ErrInvalidEnvelope) {
		t.Fatalf("tampered envelope: err = %v", err)
	}
	if _, err := p.Decrypt(ctx, uuid.New(), crypto.PurposePersonIdentifier, env); !errors.Is(err, crypto.ErrInvalidEnvelope) {
		t.Fatalf("other tenant: err = %v", err)
	}
	if _, err := p.Decrypt(ctx, tenant, crypto.PurposeOrganizationTax, env); !errors.Is(err, crypto.ErrInvalidEnvelope) {
		t.Fatalf("other purpose: err = %v", err)
	}
	if _, err := p.Decrypt(ctx, tenant, crypto.PurposePersonIdentifier, []byte{9, 9, 9}); !errors.Is(err, crypto.ErrInvalidEnvelope) {
		t.Fatalf("garbage: err = %v", err)
	}
}

func TestBlindIndexIsDeterministicAndScoped(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	i1, err := p.TenantIndex(ctx, a, crypto.PurposePersonIdentifier, "12345678901")
	if err != nil {
		t.Fatal(err)
	}
	i2, _ := p.TenantIndex(ctx, a, crypto.PurposePersonIdentifier, "12345678901")
	i3, _ := p.TenantIndex(ctx, b, crypto.PurposePersonIdentifier, "12345678901")
	i4, _ := p.TenantIndex(ctx, a, crypto.PurposeBankAccount, "12345678901")
	g1, _ := p.GlobalIndex(ctx, crypto.PurposeOrganizationTax, "1234567890")
	g2, _ := p.GlobalIndex(ctx, crypto.PurposeOrganizationTax, "1234567890")

	if len(i1) != crypto.BlindIndexSize {
		t.Fatalf("index size = %d", len(i1))
	}
	if !bytes.Equal(i1, i2) || !bytes.Equal(g1, g2) {
		t.Fatal("index must be deterministic")
	}
	if bytes.Equal(i1, i3) {
		t.Fatal("index must differ between tenants")
	}
	if bytes.Equal(i1, i4) {
		t.Fatal("index must differ between purposes")
	}
	if _, err := p.TenantIndex(ctx, uuid.Nil, crypto.PurposePersonIdentifier, "x"); err == nil {
		t.Fatal("tenant index without tenant must fail")
	}
	if _, err := p.TenantIndex(ctx, a, crypto.PurposePersonIdentifier, ""); err == nil {
		t.Fatal("empty value must fail")
	}
}

func TestNewFromEnvAndGenerate(t *testing.T) {
	t.Setenv(MasterKeyEnv, "")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("missing env must fail")
	}
	hexKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(MasterKeyEnv, hexKey)
	if _, err := NewFromEnv(); err != nil {
		t.Fatalf("generated key rejected: %v", err)
	}
	t.Setenv(MasterKeyEnv, "abcd")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("short key must fail")
	}
}
