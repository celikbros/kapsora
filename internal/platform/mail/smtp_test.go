package mail_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/platform/mail"
)

// The local defaults of .env.example, which scripts/native/up.* starts Mailpit with. They
// are read from the environment first and are only ever local.
const (
	defaultSMTPAddr     = "127.0.0.1:1025"
	defaultMailpitUIURL = "http://127.0.0.1:8025"
)

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// fakeSMTP is a one-connection SMTP server that answers whatever a test tells it to. It
// exists so the two answers the pipeline cares about — a final refusal and a server that
// is not there — are provable on a machine with no mail server running at all.
type fakeSMTP struct {
	listener net.Listener
	// replies maps an SMTP verb to the line the server answers it with. A verb that is not
	// in the map gets 250.
	replies map[string]string

	mu       sync.Mutex
	received []string
}

func startFakeSMTP(t *testing.T, replies map[string]string) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{listener: listener, replies: replies}
	go s.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return s
}

func (s *fakeSMTP) addr() string { return s.listener.Addr().String() }

func (s *fakeSMTP) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	write := func(line string) bool {
		_, err := io.WriteString(conn, line+"\r\n")
		return err == nil
	}
	if !write("220 fake ESMTP") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.Fields(strings.TrimSpace(line) + " ")[0])
		if reply, ok := s.replies[verb]; ok {
			if !write(reply) {
				return
			}
			if strings.HasPrefix(reply, "2") && verb == "DATA" {
				s.readData(reader, write)
			}
			if verb == "QUIT" {
				return
			}
			continue
		}
		switch verb {
		case "EHLO", "HELO":
			if !write("250-fake\r\n250 SIZE 10240000") {
				return
			}
		case "DATA":
			if !write("354 go ahead") {
				return
			}
			s.readData(reader, write)
		case "QUIT":
			write("221 bye")
			return
		default:
			if !write("250 ok") {
				return
			}
		}
	}
}

func (s *fakeSMTP) readData(reader *bufio.Reader, write func(string) bool) {
	var body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimRight(line, "\r\n") == "." {
			break
		}
		body.WriteString(line)
	}
	s.mu.Lock()
	s.received = append(s.received, body.String())
	s.mu.Unlock()
	write("250 2.0.0 queued")
}

func newClient(t *testing.T, addr string) *mail.SMTP {
	t.Helper()
	client, err := mail.NewSMTP(mail.SMTPOptions{
		Address: addr, From: "kapsora@kapsora.local", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new smtp client: %v", err)
	}
	return client
}

// TestSMTPSendsOneMessageAndReportsIt covers the shape of what goes on the wire: one plain
// text part, a Message-ID the caller gets back, and a subject that survives being Turkish.
func TestSMTPSendsOneMessageAndReportsIt(t *testing.T) {
	server := startFakeSMTP(t, nil)
	client := newClient(t, server.addr())

	result, err := client.Send(t.Context(), mail.Message{
		To: "ayse@example.com", Subject: "Başvurunuz onaylandı",
		Body: "Sayın Ayşe, başvurunuz onaylandı.",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.ProviderCode != mail.ProviderCodeSMTP {
		t.Fatalf("provider code = %q", result.ProviderCode)
	}
	if !strings.HasPrefix(result.ProviderMessageID, "<") || !strings.HasSuffix(result.ProviderMessageID, ">") {
		t.Fatalf("message id = %q, want an RFC 5322 identifier", result.ProviderMessageID)
	}

	bodies := server.bodies()
	if len(bodies) != 1 {
		t.Fatalf("the server received %d messages, want one", len(bodies))
	}
	raw := bodies[0]
	for _, want := range []string{
		"From: kapsora@kapsora.local", "To: ayse@example.com",
		"Content-Type: text/plain; charset=utf-8", "Auto-Submitted: auto-generated",
		"Message-ID: " + result.ProviderMessageID,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("the message is missing %q:\n%s", want, raw)
		}
	}
	// There is one part and it is text. An HTML alternative is where a tracking pixel or a
	// remote image would hide, and this module renders text and nothing else.
	if strings.Contains(strings.ToLower(raw), "text/html") {
		t.Fatalf("the message carries an HTML part:\n%s", raw)
	}
	if decoded := decodeBody(t, raw); !strings.Contains(decoded, "Sayın Ayşe") {
		t.Fatalf("the body did not survive encoding: %q", decoded)
	}
}

// TestSMTPTellsAFinalRefusalFromATemporaryOne is the distinction the whole retry policy
// rests on: a 5xx is the server's last word and a 4xx is worth trying again.
func TestSMTPTellsAFinalRefusalFromATemporaryOne(t *testing.T) {
	t.Run("a 5xx is a rejection", func(t *testing.T) {
		server := startFakeSMTP(t, map[string]string{
			"RCPT": "550 5.1.1 <ayse@example.com>: recipient rejected",
		})
		_, err := newClient(t, server.addr()).Send(t.Context(), mail.Message{
			To: "ayse@example.com", Subject: "Konu", Body: "Gövde",
		})
		if !errors.Is(err, mail.ErrRejected) {
			t.Fatalf("a 550 was reported as %v, want ErrRejected", err)
		}
		// The server quoted the recipient back at us. That quote is on its way to a stored
		// delivery row an auditor reads, so the address is gone from it.
		if strings.Contains(err.Error(), "ayse@example.com") {
			t.Fatalf("the rejection carries the recipient's address: %v", err)
		}
		if !strings.Contains(err.Error(), mail.Redacted) {
			t.Fatalf("the address was dropped rather than redacted: %v", err)
		}
	})

	t.Run("a 4xx is retryable", func(t *testing.T) {
		server := startFakeSMTP(t, map[string]string{
			"RCPT": "451 4.3.0 mailbox temporarily unavailable",
		})
		_, err := newClient(t, server.addr()).Send(t.Context(), mail.Message{
			To: "ayse@example.com", Subject: "Konu", Body: "Gövde",
		})
		if !errors.Is(err, mail.ErrUnavailable) {
			t.Fatalf("a 451 was reported as %v, want ErrUnavailable", err)
		}
		if errors.Is(err, mail.ErrRejected) {
			t.Fatal("a 451 was treated as a final refusal; the message would never be retried")
		}
	})

	t.Run("nothing listening is retryable", func(t *testing.T) {
		// Bind and close, so the port is almost certainly free and nothing answers.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := listener.Addr().String()
		_ = listener.Close()

		_, err = newClient(t, addr).Send(t.Context(), mail.Message{
			To: "ayse@example.com", Subject: "Konu", Body: "Gövde",
		})
		if !errors.Is(err, mail.ErrUnavailable) {
			t.Fatalf("a closed port was reported as %v, want ErrUnavailable", err)
		}
	})
}

// TestSMTPRefusesHeaderInjection: a newline in an address or a subject is the oldest way
// there is to add a Bcc to somebody else's mail.
func TestSMTPRefusesHeaderInjection(t *testing.T) {
	server := startFakeSMTP(t, nil)
	client := newClient(t, server.addr())

	for _, m := range []mail.Message{
		{To: "ayse@example.com\r\nBcc: attacker@example.com", Subject: "Konu", Body: "Gövde"},
		{To: "ayse@example.com", Subject: "Konu\r\nBcc: attacker@example.com", Body: "Gövde"},
		{To: "", Subject: "Konu", Body: "Gövde"},
		{To: "not-an-address", Subject: "Konu", Body: "Gövde"},
	} {
		if _, err := client.Send(t.Context(), m); !errors.Is(err, mail.ErrInvalidMessage) {
			t.Fatalf("Send(%q, %q) = %v, want ErrInvalidMessage", m.To, m.Subject, err)
		}
	}
	if got := len(server.bodies()); got != 0 {
		t.Fatalf("%d messages reached the server; a refused message must not be sent", got)
	}
}

// TestSMTPRefusesCredentialsWithoutTLS: a password sent in clear text to a relay is a
// password on the wire. The client refuses at construction rather than working and leaking.
func TestSMTPRefusesCredentialsWithoutTLS(t *testing.T) {
	_, err := mail.NewSMTP(mail.SMTPOptions{
		Address: "127.0.0.1:1025", From: "kapsora@kapsora.local",
		Username: "kapsora", Password: "hunter2",
	})
	if err == nil {
		t.Fatal("credentials without StartTLS were accepted")
	}
	if _, err := mail.NewSMTP(mail.SMTPOptions{
		Address: "127.0.0.1:1025", From: "kapsora@kapsora.local",
		Username: "kapsora", Password: "hunter2", StartTLS: true,
	}); err != nil {
		t.Fatalf("credentials with StartTLS were refused: %v", err)
	}
}

// TestRedactAddresses is the function that keeps a bounce message from putting a member's
// address in a table an auditor reads.
func TestRedactAddresses(t *testing.T) {
	in := "550 5.1.1 <ayse.yilmaz@example.com>: user unknown; cc mehmet+etiket@alt.example.co.uk"
	out := mail.RedactAddresses(in)
	for _, address := range []string{"ayse.yilmaz@example.com", "mehmet+etiket@alt.example.co.uk"} {
		if strings.Contains(out, address) {
			t.Fatalf("%q survived redaction: %q", address, out)
		}
	}
	if !strings.Contains(out, "550 5.1.1") {
		t.Fatalf("the status code was redacted away: %q", out)
	}
}

// TestSMTPAgainstMailpit is the only test in this package that talks to a real SMTP server.
// It sends through the Mailpit that scripts/native/up starts as an ordinary process
// (ADR-021: no container) and reads the message back out of Mailpit's own API, so what is
// asserted is what actually arrived rather than what the client thinks it wrote.
//
// It skips when Mailpit is not running. Everything the pipeline depends on is covered
// above against the fake server, so a machine without a mail sink still proves the
// behaviour; this test proves the wire format against a real implementation.
func TestSMTPAgainstMailpit(t *testing.T) {
	addr := envOr("KAPSORA_SMTP_ADDR", defaultSMTPAddr)
	ui := envOr("KAPSORA_MAILPIT_UI_URL", defaultMailpitUIURL)
	if !mailpitUp(t, ui) {
		t.Skipf("Mailpit is not answering on %s (scripts/native/up.*); skipping the live SMTP test", ui)
	}
	client, err := mail.NewSMTP(mail.SMTPOptions{
		Address: addr, From: "kapsora@kapsora.local", Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("new smtp client: %v", err)
	}
	if err := client.Ping(t.Context()); err != nil {
		t.Skipf("Mailpit SMTP on %s is not answering: %v", addr, err)
	}

	// A marker unique to this run, so the assertion is about this message and not one a
	// previous run left in the sink.
	marker := fmt.Sprintf("KAPSORA-TEST-%d", time.Now().UnixNano())
	subject := "Başvurunuz onaylandı " + marker
	body := "Sayın Ayşe, AUT-2026-0042 numaralı başvurunuz onaylandı. " + marker

	result, err := client.Send(t.Context(), mail.Message{
		To: "uye@example.com", Subject: subject, Body: body,
	})
	if err != nil {
		t.Fatalf("send through Mailpit: %v", err)
	}

	message := findInMailpit(t, ui, marker)
	if message.Subject != subject {
		t.Fatalf("Mailpit received subject %q, want %q", message.Subject, subject)
	}
	if !strings.Contains(message.Text, body) {
		t.Fatalf("Mailpit received body %q, want it to contain %q", message.Text, body)
	}
	if strings.TrimSpace(message.HTML) != "" {
		t.Fatalf("Mailpit received an HTML part: %q", message.HTML)
	}
	if message.MessageID != strings.Trim(result.ProviderMessageID, "<>") {
		t.Fatalf("Mailpit recorded message id %q, want %q",
			message.MessageID, strings.Trim(result.ProviderMessageID, "<>"))
	}
}

func mailpitUp(t *testing.T, base string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/livez", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// mailpitMessage is the part of Mailpit's message representation this test reads.
type mailpitMessage struct {
	ID        string `json:"ID"`
	MessageID string `json:"MessageID"`
	Subject   string `json:"Subject"`
	Text      string `json:"Text"`
	HTML      string `json:"HTML"`
}

// findInMailpit polls the sink for the message carrying the marker. Mailpit accepts the
// message at the end of DATA and indexes it a moment later, so a single read can miss it.
func findInMailpit(t *testing.T, base, marker string) mailpitMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var listing struct {
			Messages []mailpitMessage `json:"messages"`
		}
		getJSON(t, base+"/api/v1/messages?limit=50", &listing)
		for _, summary := range listing.Messages {
			if !strings.Contains(summary.Subject, marker) {
				continue
			}
			var full mailpitMessage
			getJSON(t, base+"/api/v1/message/"+summary.ID, &full)
			return full
		}
		if time.Now().After(deadline) {
			t.Fatalf("the message carrying %s never appeared in Mailpit", marker)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func getJSON(t *testing.T, url string, dst any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

// decodeBody pulls the base64 part out of what the fake server received.
func decodeBody(t *testing.T, raw string) string {
	t.Helper()
	_, encoded, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("the message has no body:\n%s", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(encoded), "\r\n", ""))
	if err != nil {
		t.Fatalf("decode the body: %v", err)
	}
	return string(decoded)
}
