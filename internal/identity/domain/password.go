package domain

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password storage uses Argon2id with the OWASP baseline parameters. The encoded form is
// the standard PHC string, so the parameters travel with every hash and can be raised
// later without invalidating existing passwords (VerifyPassword reports needsRehash).
//
//	$argon2id$v=19$m=19456,t=2,p=1$<base64 salt>$<base64 hash>

// PasswordParams are the Argon2id cost parameters.
type PasswordParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultPasswordParams follows the OWASP Password Storage Cheat Sheet baseline.
func DefaultPasswordParams() PasswordParams {
	return PasswordParams{MemoryKiB: 19456, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

// Password policy limits. The minimum follows the "long passphrase, no composition rules"
// guidance and is counted in characters. The maximum is counted in BYTES and is
// deliberately generous: it exists only so a huge body cannot be pushed through the
// hasher. A cap measured in characters would be a quieter cap for Turkish than for
// English, because the same sentence costs more bytes in Turkish.
const (
	MinPasswordLength    = 12
	MaxPasswordSizeBytes = 1024
)

// Bounds applied to a stored hash before its parts are used.
const (
	minSaltBytes     = 8
	minKeyBytes      = 16
	maxHashPartBytes = 1024
)

// Errors returned by password handling.
var (
	ErrPasswordTooShort  = fmt.Errorf("identity: password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong   = fmt.Errorf("identity: password must be at most %d bytes", MaxPasswordSizeBytes)
	ErrPasswordTooCommon = errors.New("identity: password must not contain the user name")
	ErrInvalidHash       = errors.New("identity: stored password hash is malformed")
)

// ValidatePassword enforces the policy on a new password.
func ValidatePassword(plain, username string) error {
	switch {
	case utf8.RuneCountInString(plain) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(plain) > MaxPasswordSizeBytes:
		return ErrPasswordTooLong
	}
	if username != "" {
		local, _, _ := strings.Cut(strings.ToLower(username), "@")
		lower := strings.ToLower(plain)
		if len(local) >= 4 && strings.Contains(lower, local) {
			return ErrPasswordTooCommon
		}
	}
	return nil
}

// HashPassword returns the PHC-encoded Argon2id hash of plain.
func HashPassword(plain string, p PasswordParams) (string, error) {
	if len(plain) > MaxPasswordSizeBytes {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword compares plain against an encoded hash in constant time. needsRehash is
// true when the stored hash used weaker parameters than the ones supplied.
func VerifyPassword(plain, encoded string, want PasswordParams) (ok bool, needsRehash bool, err error) {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	candidate := argon2.IDKey([]byte(plain), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength)
	if subtle.ConstantTimeCompare(candidate, key) != 1 {
		return false, false, nil
	}
	weaker := p.MemoryKiB < want.MemoryKiB || p.Iterations < want.Iterations || p.KeyLength < want.KeyLength
	return true, weaker, nil
}

// dummyHash is verified against when the user does not exist, so that a wrong user name
// and a wrong password take the same time and cannot be told apart by an attacker.
var dummyHash = func() string {
	h, err := HashPassword("kapsora-timing-equaliser", DefaultPasswordParams())
	if err != nil {
		panic(err)
	}
	return h
}()

// BurnPasswordTime performs the same work as a real verification and discards the result.
func BurnPasswordTime(plain string) {
	_, _, _ = VerifyPassword(plain, dummyHash, DefaultPasswordParams())
}

func decodeHash(encoded string) (p PasswordParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	// Bound the sizes before converting: a stored row is untrusted input, and an absurd
	// salt or digest length must be a malformed-hash error rather than a huge allocation.
	if len(salt) < minSaltBytes || len(salt) > maxHashPartBytes ||
		len(key) < minKeyBytes || len(key) > maxHashPartBytes {
		return p, nil, nil, ErrInvalidHash
	}
	p.SaltLength = uint32(len(salt)) //nolint:gosec // bounded above
	p.KeyLength = uint32(len(key))   //nolint:gosec // bounded above
	return p, salt, key, nil
}

// NormalizeUsername makes login case-insensitive and whitespace-tolerant.
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
