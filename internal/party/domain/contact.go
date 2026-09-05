package domain

import (
	"fmt"
	"strings"
)

// Contact channels (party.person_contact.channel, migration 000035).
const (
	ChannelEmail = "EMAIL"
	ChannelSMS   = "SMS"
)

// Field error codes of a contact.
const (
	CodeContactChannelUnknown = "CONTACT_CHANNEL_UNKNOWN"
	CodeContactInvalid        = "CONTACT_INVALID"
	CodeContactPrimaryTwice   = "CONTACT_PRIMARY_TWICE"
)

// MaxContacts bounds one replace. Twenty ways of reaching one person is already more than
// anybody maintains; it is the contract's maxItems.
const MaxContacts = 20

// SubmittedContact is one contact as it arrives from a caller.
type SubmittedContact struct {
	Channel  string
	Value    string
	Primary  bool
	Verified bool
}

// NormalizeContact puts a value into the one form the platform stores it in, so the same
// address written two ways is the same address. An e-mail address is trimmed and
// lower-cased — the domain is case-insensitive and no mail server anybody uses treats the
// local part otherwise. A telephone number keeps a leading `+` and loses every space,
// dash, dot and bracket, because those are typography rather than digits.
func NormalizeContact(channel, raw string) string {
	value := strings.TrimSpace(raw)
	switch channel {
	case ChannelEmail:
		return strings.ToLower(value)
	case ChannelSMS:
		var b strings.Builder
		for i, r := range value {
			switch {
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			case r == '+' && i == 0:
				b.WriteRune(r)
			}
		}
		return b.String()
	default:
		return value
	}
}

// ValidContactChannel reports whether the channel is one the schema accepts.
func ValidContactChannel(channel string) bool {
	return channel == ChannelEmail || channel == ChannelSMS
}

// ValidateContact checks a normalised value against the shape its channel promises.
//
// The checks are deliberately shallow. An address is proved by sending to it, which is
// M10's onboarding; what this refuses is the value nobody could ever send to — an e-mail
// address with no `@` or no dot after it, a telephone number too short or too long to be
// one. Refusing more would refuse real addresses, and a member who cannot be recorded is
// worse off than a member whose number turns out to be wrong.
func ValidateContact(channel, normalized string) error {
	switch channel {
	case ChannelEmail:
		at := strings.LastIndex(normalized, "@")
		host := ""
		if at > 0 {
			host = normalized[at+1:]
		}
		if at <= 0 || len(normalized) > 254 ||
			strings.ContainsAny(normalized, " \t\r\n,;\"<>()[]\\") ||
			!strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return fmt.Errorf("%w: e-posta adresi", errContactShape)
		}
		return nil
	case ChannelSMS:
		digits := strings.TrimPrefix(normalized, "+")
		if len(digits) < 7 || len(digits) > 15 {
			return fmt.Errorf("%w: telefon numarası", errContactShape)
		}
		for _, r := range digits {
			if r < '0' || r > '9' {
				return fmt.Errorf("%w: telefon numarası", errContactShape)
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: kanal", errContactShape)
	}
}

// errContactShape is the sentinel behind every ValidateContact failure; the caller turns
// it into a field error rather than showing it.
var errContactShape = errContact("party: contact value does not have the shape of its channel")

type errContact string

func (e errContact) Error() string { return string(e) }

// MaskContact is what a screen, a log and an audit row are allowed to see.
//
// An e-mail keeps its first character and its domain, because "a***@example.com" is what
// lets an operator on the telephone say "the address ending example.com" without reading
// anybody's address aloud. A telephone number keeps its last two digits and nothing else,
// which is the same rule the identifier masks follow: enough to recognise a value you
// already know, never enough to learn one you do not.
func MaskContact(channel, normalized string) string {
	switch channel {
	case ChannelEmail:
		at := strings.LastIndex(normalized, "@")
		if at <= 0 {
			return "**"
		}
		local, host := normalized[:at], normalized[at:]
		if len(local) <= 1 {
			return strings.Repeat("*", 3) + host
		}
		return local[:1] + strings.Repeat("*", len(local)-1) + host
	case ChannelSMS:
		plus := ""
		digits := normalized
		if strings.HasPrefix(digits, "+") {
			plus, digits = "+", digits[1:]
		}
		if len(digits) <= 2 {
			return plus + strings.Repeat("*", len(digits))
		}
		return plus + strings.Repeat("*", len(digits)-2) + digits[len(digits)-2:]
	default:
		return "**"
	}
}
