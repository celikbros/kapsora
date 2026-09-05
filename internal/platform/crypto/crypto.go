// Package crypto defines the field-level encryption and blind-index ports used for
// sensitive identifiers (v1.2 section 16.14). Plaintext identifiers never reach the
// database: columns hold an envelope (*_cipher) plus an HMAC blind index (*_hash).
//
// Implementations: localkey (development, tests, single-node pilots) and, later, a
// KMS/Vault-backed cipher. Callers depend only on these interfaces.
package crypto

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Purpose namespaces key derivation so values of different kinds can never be compared
// or decrypted with each other's keys.
type Purpose string

const (
	PurposePersonIdentifier Purpose = "person.identifier"
	PurposeOrganizationTax  Purpose = "organization.tax_number"
	PurposeBankAccount      Purpose = "billing.bank_account"
	PurposeIntegratorSecret Purpose = "fiscal.integrator_secret"
	// PurposePractitionerRegistration namespaces the professional registration number of
	// a provider practitioner (WP-I3-02). It is a separate purpose from a person
	// identifier so the same digits registered as a TCKN and as a registration number
	// never produce the same blind index.
	PurposePractitionerRegistration Purpose = "provider.practitioner_registration"
	// PurposePersonContact namespaces a member's e-mail address or telephone number
	// (WP-I5-05). It is separate from a person identifier so a number recorded as a
	// member number and the same digits recorded as a telephone number are encrypted
	// under different keys and can never be compared.
	PurposePersonContact Purpose = "party.person_contact"
)

// BlindIndexSize is the byte length of every blind index (HMAC-SHA256).
const BlindIndexSize = 32

// ErrInvalidEnvelope is returned when ciphertext is malformed, tampered with or was
// produced by an unknown key version.
var ErrInvalidEnvelope = errors.New("crypto: invalid envelope")

// FieldCipher envelope-encrypts one field value. The envelope embeds the key id so keys
// can rotate with dual-read; tenant and purpose are bound into the key derivation.
type FieldCipher interface {
	Encrypt(ctx context.Context, tenantID uuid.UUID, purpose Purpose, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, tenantID uuid.UUID, purpose Purpose, envelope []byte) ([]byte, error)
}

// BlindIndexer derives deterministic BlindIndexSize-byte indexes for equality search.
// Callers normalise the input first (trim, upper-case, strip separators).
//
// TenantIndex binds the index to a tenant: the same TCKN yields different indexes in
// different tenants. GlobalIndex is platform-wide and used only for the shared
// organization directory (country + tax number).
type BlindIndexer interface {
	TenantIndex(ctx context.Context, tenantID uuid.UUID, purpose Purpose, normalized string) ([]byte, error)
	GlobalIndex(ctx context.Context, purpose Purpose, normalized string) ([]byte, error)
}
