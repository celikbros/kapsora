// Package localkey implements crypto.FieldCipher and crypto.BlindIndexer from a single
// 32-byte master key held in memory. Per-tenant, per-purpose keys are derived with
// HKDF-SHA256, so a leaked derived key never exposes another tenant or purpose.
//
// Suitable for development, tests and single-node pilots where the master key is
// injected from a secret file or environment variable. Production deployments swap in a
// KMS/Vault-backed implementation behind the same interfaces (ADR-020).
package localkey

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/crypto"
)

const (
	// MasterKeyEnv holds the 64-hex-character master key for local/pilot deployments.
	MasterKeyEnv = "KAPSORA_LOCAL_MASTER_KEY"

	masterKeySize = 32
	envelopeV1    = byte(1)
	keyIDLocal    = byte(1)
	nonceSize     = 12
	derivedSize   = 32
)

// Provider derives keys from one master key.
type Provider struct {
	master []byte
}

// New returns a provider for a 32-byte master key.
func New(masterKey []byte) (*Provider, error) {
	if len(masterKey) != masterKeySize {
		return nil, fmt.Errorf("localkey: master key must be %d bytes, got %d", masterKeySize, len(masterKey))
	}
	p := &Provider{master: make([]byte, masterKeySize)}
	copy(p.master, masterKey)
	return p, nil
}

// NewFromEnv reads MasterKeyEnv (hex). It fails when the variable is missing so a
// deployment can never silently run with an empty key.
func NewFromEnv() (*Provider, error) {
	raw := strings.TrimSpace(os.Getenv(MasterKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("localkey: %s is not set", MasterKeyEnv)
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("localkey: %s must be hex: %w", MasterKeyEnv, err)
	}
	return New(key)
}

// GenerateMasterKey returns a fresh random key as hex, for bootstrapping a local .env.
func GenerateMasterKey() (string, error) {
	key := make([]byte, masterKeySize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

var _ crypto.FieldCipher = (*Provider)(nil)
var _ crypto.BlindIndexer = (*Provider)(nil)

// Encrypt returns: version(1) | keyID(1) | nonce(12) | AES-256-GCM ciphertext+tag.
// tenantID and purpose are bound both into the derived key and into the GCM
// additional data, so an envelope moved to another tenant/column fails to decrypt.
func (p *Provider) Encrypt(_ context.Context, tenantID uuid.UUID, purpose crypto.Purpose, plaintext []byte) ([]byte, error) {
	aead, err := p.aead("cipher", tenantID, purpose)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("localkey: nonce: %w", err)
	}
	out := make([]byte, 0, 2+nonceSize+len(plaintext)+aead.Overhead())
	out = append(out, envelopeV1, keyIDLocal)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, additionalData(tenantID, purpose)), nil
}

// Decrypt reverses Encrypt and reports crypto.ErrInvalidEnvelope on any failure so
// callers never leak whether tampering or a wrong key caused it.
func (p *Provider) Decrypt(_ context.Context, tenantID uuid.UUID, purpose crypto.Purpose, envelope []byte) ([]byte, error) {
	if len(envelope) < 2+nonceSize || envelope[0] != envelopeV1 || envelope[1] != keyIDLocal {
		return nil, crypto.ErrInvalidEnvelope
	}
	aead, err := p.aead("cipher", tenantID, purpose)
	if err != nil {
		return nil, err
	}
	nonce := envelope[2 : 2+nonceSize]
	plaintext, err := aead.Open(nil, nonce, envelope[2+nonceSize:], additionalData(tenantID, purpose))
	if err != nil {
		return nil, crypto.ErrInvalidEnvelope
	}
	return plaintext, nil
}

// TenantIndex is HMAC-SHA256 under a key derived for (tenant, purpose).
func (p *Provider) TenantIndex(_ context.Context, tenantID uuid.UUID, purpose crypto.Purpose, normalized string) ([]byte, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("localkey: tenant index requires a tenant id")
	}
	return p.index("blind-index", tenantID, purpose, normalized)
}

// GlobalIndex is HMAC-SHA256 under a platform-wide key derived for the purpose only.
func (p *Provider) GlobalIndex(_ context.Context, purpose crypto.Purpose, normalized string) ([]byte, error) {
	return p.index("blind-index-global", uuid.Nil, purpose, normalized)
}

func (p *Provider) index(domain string, tenantID uuid.UUID, purpose crypto.Purpose, normalized string) ([]byte, error) {
	if normalized == "" {
		return nil, errors.New("localkey: empty value cannot be indexed")
	}
	key, err := p.derive(domain, tenantID, purpose)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(normalized))
	return mac.Sum(nil), nil
}

func (p *Provider) aead(domain string, tenantID uuid.UUID, purpose crypto.Purpose) (cipher.AEAD, error) {
	key, err := p.derive(domain, tenantID, purpose)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("localkey: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("localkey: gcm: %w", err)
	}
	return aead, nil
}

func (p *Provider) derive(domain string, tenantID uuid.UUID, purpose crypto.Purpose) ([]byte, error) {
	info := domain + "|" + string(purpose) + "|" + tenantID.String()
	key, err := hkdf.Key(sha256.New, p.master, []byte("kapsora-localkey-v1"), info, derivedSize)
	if err != nil {
		return nil, fmt.Errorf("localkey: hkdf: %w", err)
	}
	return key, nil
}

func additionalData(tenantID uuid.UUID, purpose crypto.Purpose) []byte {
	return []byte(tenantID.String() + "|" + string(purpose))
}
