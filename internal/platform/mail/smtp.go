package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProviderCodeSMTP is what an attempt through this client is recorded under.
const ProviderCodeSMTP = "SMTP"

// SMTP speaks plain SMTP over TCP. Mailpit runs as a native process locally and a
// customer's relay in production, so the address is a host:port from the environment.
//
// The client is deliberately thin. It opens a connection per message, writes one plain
// text part and turns the server's reply into one of two errors. Everything that decides
// what to do about that answer — whether to record an attempt, whether to retry, what the
// message's status becomes — lives in the notification pipeline, which is what lets the
// pipeline be tested without a mail server.
type SMTP struct {
	addr     string
	host     string
	from     string
	username string
	password string
	startTLS bool
	timeout  time.Duration
	dialer   *net.Dialer
}

// SMTPOptions configures the client.
type SMTPOptions struct {
	// Address is host:port of the SMTP server, for example 127.0.0.1:1025.
	Address string
	// From is the envelope and header sender. It has to be an address the relay accepts
	// as its own, so it is configuration rather than something a caller may choose.
	From string
	// Username and Password authenticate to the relay. They are only ever sent over a
	// TLS connection: Send refuses to authenticate in clear text rather than quietly
	// sending the password anyway.
	Username string
	Password string
	// StartTLS upgrades the connection when the server offers it. It is off for the local
	// Mailpit, which offers no TLS at all, and on for every relay.
	StartTLS bool
	// Timeout bounds one whole exchange, connection included; default 30 seconds.
	Timeout time.Duration
}

// NewSMTP validates the options and returns the client. It does not connect: a relay that
// is down at start-up must not stop the worker from starting, because the messages it
// cannot send simply stay queued and are retried.
func NewSMTP(o SMTPOptions) (*SMTP, error) {
	addr := strings.TrimSpace(o.Address)
	if addr == "" {
		return nil, errors.New("mail: the SMTP address is required")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("mail: SMTP address %q must be host:port: %w", addr, err)
	}
	from := strings.TrimSpace(o.From)
	if err := validHeaderValue("from", from); err != nil {
		return nil, err
	}
	if !strings.Contains(from, "@") {
		return nil, fmt.Errorf("mail: the sender %q is not an address", from)
	}
	if o.Username != "" && !o.StartTLS {
		// A password sent in clear text to a relay is a password on the wire. Refusing at
		// construction is the honest answer; the alternative is a client that works and
		// leaks.
		return nil, errors.New("mail: SMTP credentials require StartTLS")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &SMTP{
		addr: addr, host: host, from: from,
		username: o.Username, password: o.Password, startTLS: o.StartTLS,
		timeout: timeout, dialer: &net.Dialer{Timeout: timeout},
	}, nil
}

var _ Sender = (*SMTP)(nil)

// Send delivers one message and reports what the server said.
func (s *SMTP) Send(ctx context.Context, m Message) (Result, error) {
	if err := validRecipient(m.To); err != nil {
		return Result{}, err
	}
	if err := validHeaderValue("subject", m.Subject); err != nil {
		return Result{}, err
	}
	messageID := fmt.Sprintf("<%s@%s>", uuid.NewString(), senderDomain(s.from))
	payload := s.compose(m, messageID)

	client, conn, err := s.connect(ctx)
	if err != nil {
		return Result{}, err
	}
	defer closeQuietly(client)
	defer closeQuietly(conn)

	if err := s.negotiate(client); err != nil {
		return Result{}, err
	}
	if err := client.Mail(s.from); err != nil {
		return Result{}, classify("MAIL FROM", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return Result{}, classify("RCPT TO", err)
	}
	w, err := client.Data()
	if err != nil {
		return Result{}, classify("DATA", err)
	}
	if _, err := w.Write(payload); err != nil {
		closeQuietly(w)
		return Result{}, classify("write body", err)
	}
	if err := w.Close(); err != nil {
		return Result{}, classify("end of data", err)
	}
	// A failed QUIT is not a failed delivery: the server has already accepted the message
	// at the end of DATA, and reporting an error here would have the pipeline send it
	// again.
	_ = client.Quit()
	return Result{
		ProviderCode: ProviderCodeSMTP, ProviderMessageID: messageID,
		Detail: "accepted at end of data",
	}, nil
}

// Ping opens a connection and says nothing else. It is what the readiness probe asks.
func (s *SMTP) Ping(ctx context.Context) error {
	client, conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer closeQuietly(conn)
	if err := client.Noop(); err != nil {
		closeQuietly(client)
		return classify("NOOP", err)
	}
	return client.Quit()
}

// connect dials and greets, applying whichever deadline is nearer: the caller's context or
// this client's own timeout. net/smtp predates contexts, so the deadline on the socket is
// how a cancelled context reaches the exchange.
func (s *SMTP) connect(ctx context.Context) (*smtp.Client, net.Conn, error) {
	conn, err := s.dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dial %s: %w", ErrUnavailable, s.addr, err)
	}
	deadline := time.Now().Add(s.timeout)
	if fromCtx, ok := ctx.Deadline(); ok && fromCtx.Before(deadline) {
		deadline = fromCtx
	}
	if err := conn.SetDeadline(deadline); err != nil {
		closeQuietly(conn)
		return nil, nil, fmt.Errorf("%w: set deadline: %w", ErrUnavailable, err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		closeQuietly(conn)
		return nil, nil, classify("greeting", err)
	}
	return client, conn, nil
}

// negotiate upgrades the connection and authenticates, in that order. Neither is
// attempted when the server does not offer it and the configuration does not need it,
// which is how the same client talks to Mailpit and to a real relay.
func (s *SMTP) negotiate(client *smtp.Client) error {
	if s.startTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("%w: the server does not offer STARTTLS", ErrUnavailable)
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
			return classify("STARTTLS", err)
		}
	}
	if s.username == "" {
		return nil
	}
	if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return classify("AUTH", err)
	}
	return nil
}

// compose builds the RFC 5322 message. The body is base64 so a long Turkish sentence is
// neither wrapped in the middle of a rune nor mangled by a relay that rewrites line
// endings.
func (s *SMTP) compose(m Message, messageID string) []byte {
	var b strings.Builder
	b.WriteString("From: " + s.from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// A notification is never a reply and never wants one: an auto-responder answering a
	// no-reply address is a loop, and a bulk header is what tells one not to.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(m.Body))
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return []byte(b.String())
}

// classify turns a server reply into one of the two errors the pipeline tells apart. A
// 5xx is the server's final word and a retry would only repeat it; everything else,
// including a 4xx and a broken socket, is worth trying again. The reply text is redacted
// and bounded before it is wrapped, because it is on its way to a stored delivery row.
func classify(step string, err error) error {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		detail := Truncate(RedactAddresses(protoErr.Msg), MaxDetail)
		if protoErr.Code >= 500 && protoErr.Code < 600 {
			return fmt.Errorf("%w: %s: %d %s", ErrRejected, step, protoErr.Code, detail)
		}
		return fmt.Errorf("%w: %s: %d %s", ErrUnavailable, step, protoErr.Code, detail)
	}
	return fmt.Errorf("%w: %s: %s", ErrUnavailable, step, Truncate(RedactAddresses(err.Error()), MaxDetail))
}

// MaxDetail bounds what a server's answer may contribute to a delivery row; the column
// itself is CHECKed at 500 characters.
const MaxDetail = 300

// validRecipient refuses an address that would let the caller write its own headers. A
// newline in an address is the oldest way there is to add a Bcc to somebody else's mail.
func validRecipient(to string) error {
	address := strings.TrimSpace(to)
	if address == "" {
		return fmt.Errorf("%w: no recipient", ErrInvalidMessage)
	}
	if !strings.Contains(address, "@") {
		return fmt.Errorf("%w: %q is not an address", ErrInvalidMessage, RedactAddresses(address))
	}
	return validHeaderValue("to", address)
}

func validHeaderValue(field, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%w: the %s header contains a line break", ErrInvalidMessage, field)
	}
	return nil
}

// senderDomain is the right hand side of the configured sender, used to build a
// Message-ID that belongs to this installation.
func senderDomain(from string) string {
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		return from[at+1:]
	}
	return "kapsora.local"
}
