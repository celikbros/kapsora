package channel_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/notification/infrastructure/channel"
	"github.com/celikbros/kapsora/internal/platform/mail"
)

// stubSender answers whatever the test tells it to and remembers what it was handed.
type stubSender struct {
	result mail.Result
	err    error
	last   mail.Message
	calls  int
}

func (s *stubSender) Send(_ context.Context, m mail.Message) (mail.Result, error) {
	s.calls++
	s.last = m
	return s.result, s.err
}

func (s *stubSender) Ping(context.Context) error { return s.err }

func message(t *testing.T) application.MessageRecord {
	t.Helper()
	subject := "Başvurunuz onaylandı"
	body := "Sayın Ayşe, AUT-2026-0042 numaralı başvurunuz onaylandı."
	return application.MessageRecord{
		ID: uuid.New(), EventCode: "authorization.approved",
		RecipientType: domain.RecipientActor, RecipientID: uuid.New(),
		Channel: domain.ChannelEmail, Locale: "tr-TR",
		SubjectRendered: &subject, BodyRendered: &body,
		Status: domain.MessageQueued,
	}
}

// TestEmailTurnsAServerAnswerIntoAnOutcome is the glue the retry policy rests on: a
// refusal has to arrive as a REJECTED result the pipeline records and stops on, and a
// server that could not be reached has to arrive as an error the pipeline retries. Getting
// these two the wrong way round would either retry a refusal forever or drop a message
// because a relay was restarting.
func TestEmailTurnsAServerAnswerIntoAnOutcome(t *testing.T) {
	t.Run("an accepted message", func(t *testing.T) {
		stub := &stubSender{result: mail.Result{
			ProviderCode:      mail.ProviderCodeSMTP,
			ProviderMessageID: "<abc@kapsora.local>", Detail: "accepted at end of data",
		}}
		adapter, err := channel.NewEmail(stub)
		if err != nil {
			t.Fatal(err)
		}
		result, err := adapter.Send(t.Context(), "ayse@example.com", message(t))
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if result.Outcome != domain.OutcomeAccepted {
			t.Fatalf("outcome = %s, want ACCEPTED", result.Outcome)
		}
		if result.ProviderCode != channel.ProviderSMTP || result.ProviderMessageID != "<abc@kapsora.local>" {
			t.Fatalf("result = %+v", result)
		}
		// The subject and the body the message carries are what goes on the wire, and the
		// address comes from the caller rather than from the row: it is resolved at send
		// time and never stored.
		if stub.last.To != "ayse@example.com" ||
			!strings.Contains(stub.last.Body, "AUT-2026-0042") ||
			stub.last.Subject != "Başvurunuz onaylandı" {
			t.Fatalf("the adapter handed over %+v", stub.last)
		}
	})

	for _, c := range []struct {
		name string
		err  error
	}{
		{"a refusal", fmt.Errorf("%w: RCPT TO: 550 recipient rejected", mail.ErrRejected)},
		{"a message this package will not send", fmt.Errorf("%w: no recipient", mail.ErrInvalidMessage)},
	} {
		t.Run(c.name+" is a result, not an error", func(t *testing.T) {
			adapter, err := channel.NewEmail(&stubSender{err: c.err})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Send(t.Context(), "ayse@example.com", message(t))
			if err != nil {
				t.Fatalf("a refusal was reported as an error: %v", err)
			}
			if result.Outcome != domain.OutcomeRejected {
				t.Fatalf("outcome = %s, want REJECTED", result.Outcome)
			}
			if result.Detail == "" {
				t.Fatal("a rejection with no detail; an operator cannot see why")
			}
		})
	}

	t.Run("a server that could not be reached is an error", func(t *testing.T) {
		wanted := fmt.Errorf("%w: dial: connection refused", mail.ErrUnavailable)
		adapter, err := channel.NewEmail(&stubSender{err: wanted})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Send(t.Context(), "ayse@example.com", message(t)); !errors.Is(err, wanted) {
			t.Fatalf("an unreachable server was reported as %v, want the error through", err)
		}
	})

	t.Run("a message with no body is never sent", func(t *testing.T) {
		stub := &stubSender{}
		adapter, err := channel.NewEmail(stub)
		if err != nil {
			t.Fatal(err)
		}
		empty := message(t)
		empty.BodyRendered = nil
		if _, err := adapter.Send(t.Context(), "ayse@example.com", empty); err == nil {
			t.Fatal("a message with no body was accepted")
		}
		if stub.calls != 0 {
			t.Fatal("a message with no body reached the mail server")
		}
	})

	if _, err := channel.NewEmail(nil); err == nil {
		t.Fatal("the e-mail adapter was built without a sender")
	}
}

// TestRecordersSayTheyDeliveredNothing: the SMS stub, the push recorder and the in-app
// recorder all accept, and the provider code on the attempt is the whole of the difference
// between "a mail server took this" and "nothing outside this process has seen it".
func TestRecordersSayTheyDeliveredNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		adapter      application.ChannelSender
		providerCode string
	}{
		{channel.NewSMS(logger), channel.ProviderSMS},
		{channel.NewPush(logger), channel.ProviderPush},
		{channel.NewInApp(logger), channel.ProviderInApp},
	} {
		m := message(t)
		result, err := c.adapter.Send(t.Context(), "", m)
		if err != nil {
			t.Fatalf("%s: %v", c.providerCode, err)
		}
		if result.ProviderCode != c.providerCode {
			t.Fatalf("provider code = %q, want %q", result.ProviderCode, c.providerCode)
		}
		if result.Outcome != domain.OutcomeAccepted {
			t.Fatalf("%s outcome = %s", c.providerCode, result.Outcome)
		}
		if !strings.Contains(result.ProviderMessageID, m.ID.String()) {
			t.Fatalf("%s message id = %q, want it to name the message", c.providerCode, result.ProviderMessageID)
		}
		if result.Detail == "" {
			t.Fatalf("%s recorded nothing about what it did", c.providerCode)
		}
	}
}
