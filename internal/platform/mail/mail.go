// Package mail is the outgoing e-mail port of the notification module. Locally it talks
// to Mailpit, which scripts/native/up starts as an ordinary process on 127.0.0.1:1025
// (ADR-021: there is no container anywhere in this project); in production it talks to
// the customer's own relay.
//
// The port is an interface for a reason that is not abstraction for its own sake: the
// guarantees the notification pipeline has to prove — an attempt is recorded whatever the
// server said, a permanent rejection stops the retries, a bounce message never leaves an
// address in the delivery log — have to be provable on a machine with no mail server
// running. A test drives a sender that rejects on demand; the SMTP client below is then
// only responsible for turning a socket conversation into one of three answers.
package mail

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
)

// Message is one outgoing e-mail. Body is plain UTF-8 text: the notification module
// renders text and nothing else, so there is no HTML part for a tracking pixel or a
// remote image to hide in.
type Message struct {
	// To is the recipient address. It never reaches the database: it is resolved at send
	// time and forgotten again.
	To string
	// Subject is one line. A carriage return in it is refused rather than sent, because
	// that is how a header is injected.
	Subject string
	Body    string
}

// Result is what the server said about a message it took. ProviderMessageID is the
// Message-ID the client generated, which is the only handle either side has on the
// message afterwards.
type Result struct {
	ProviderCode      string
	ProviderMessageID string
	// Detail is a short, bounded description of the exchange with the addresses removed.
	Detail string
}

// Errors the pipeline tells apart. Everything else is treated as unavailable, because an
// unrecognised failure is more likely to be a network than a refusal.
var (
	// ErrRejected is the server refusing the message for good: a 5xx reply, a recipient it
	// will not accept, a body it will not take. Retrying changes nothing.
	ErrRejected = errors.New("mail: the server rejected the message")
	// ErrUnavailable is the server not answering, or answering 4xx. It is worth retrying.
	ErrUnavailable = errors.New("mail: the mail server is unavailable")
	// ErrInvalidMessage is a message this package refuses to send: an address or a subject
	// with a line break in it, or an empty recipient.
	ErrInvalidMessage = errors.New("mail: the message cannot be sent as written")
)

// Sender hands one message to a mail server. An implementation must return ErrRejected
// only when a retry is genuinely pointless: everything else keeps the message retryable,
// because a message that is dropped because a relay was restarting is a member who was
// never told.
type Sender interface {
	Send(ctx context.Context, m Message) (Result, error)
	// Ping reports whether the server is answering; the readiness probe uses it.
	Ping(ctx context.Context) error
}

// addressPattern is deliberately loose. It is used to take addresses out of a server's
// reply before that reply is written to the delivery log, and being too eager there costs
// nothing: a redacted word in an error message is a smaller problem than a member's
// address in a table an auditor reads.
var addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)

// Redacted is the placeholder an address is replaced with.
const Redacted = "<gizlendi>"

// RedactAddresses removes e-mail addresses from a server's reply. A bounce quotes the
// recipient — "550 5.1.1 <ayse@example.com>: user unknown" — and that quote is exactly
// what would otherwise end up stored in notification.delivery.detail forever.
func RedactAddresses(s string) string {
	return addressPattern.ReplaceAllString(s, Redacted)
}

// Truncate bounds a provider's answer to n bytes on a rune boundary.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	out := s[:n]
	for len(out) > 0 && !isRuneStart(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return strings.TrimSpace(out)
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// closeQuietly closes what a failed exchange left open. The error is deliberately
// dropped: the failure the caller is about to report is the one worth reporting, and a
// second error from closing a socket that is already broken only hides it.
func closeQuietly(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
}
