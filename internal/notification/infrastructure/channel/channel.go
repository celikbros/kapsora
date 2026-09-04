// Package channel holds the adapters that hand a rendered message to something outside
// the platform. There are four channels and, in this milestone, three kinds of adapter.
//
// EMAIL goes through SMTP: Mailpit locally, the customer's relay in production.
//
// SMS is a stub. No provider has been chosen, and a stub that records the attempt is a
// better answer than either pretending a message went or refusing to queue one: when a
// provider is chosen, the messages are already in the log with a delivery row saying they
// were never handed to anybody.
//
// PUSH and INAPP are recorded and not delivered. INAPP is arguably delivered by being
// recorded — the message log is the inbox a screen would read — and PUSH has no device
// registry yet. Both write a delivery row naming the recorder rather than a provider, so
// nobody reading the log can mistake one for a message that reached a phone.
package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/mail"
)

// Provider codes recorded on a delivery attempt. They are the difference between "a mail
// server took this" and "nothing outside this process has seen it".
const (
	ProviderSMTP  = "SMTP"
	ProviderSMS   = "SMS_STUB"
	ProviderPush  = "PUSH_RECORDER"
	ProviderInApp = "INAPP_RECORDER"
)

// Email hands a message to an SMTP server.
type Email struct {
	sender mail.Sender
}

// NewEmail wires the e-mail adapter.
func NewEmail(sender mail.Sender) (*Email, error) {
	if sender == nil {
		return nil, errors.New("notification: the e-mail channel needs a sender")
	}
	return &Email{sender: sender}, nil
}

var _ application.ChannelSender = (*Email)(nil)

// Send implements application.ChannelSender.
//
// A rejection is a result rather than an error: the pipeline records it as an attempt and
// stops retrying, which is a fact worth having in the log. Only a server that could not be
// reached is an error, because that is the one case where nobody knows what happened.
func (e *Email) Send(ctx context.Context, address string, m application.MessageRecord) (
	application.SenderResult, error,
) {
	if m.BodyRendered == nil {
		return application.SenderResult{}, errors.New("notification: the message has no body")
	}
	subject := ""
	if m.SubjectRendered != nil {
		subject = *m.SubjectRendered
	}
	result, err := e.sender.Send(ctx, mail.Message{
		To: address, Subject: subject, Body: *m.BodyRendered,
	})
	switch {
	case err == nil:
		return application.SenderResult{
			ProviderCode: ProviderSMTP, ProviderMessageID: result.ProviderMessageID,
			Outcome: domain.OutcomeAccepted, Detail: result.Detail,
		}, nil
	case errors.Is(err, mail.ErrRejected), errors.Is(err, mail.ErrInvalidMessage):
		return application.SenderResult{
			ProviderCode: ProviderSMTP, Outcome: domain.OutcomeRejected,
			// The reply is already redacted and bounded by the SMTP client; saying it
			// again here would be the second place an address could leak.
			Detail: mail.Truncate(mail.RedactAddresses(err.Error()), mail.MaxDetail),
		}, nil
	default:
		return application.SenderResult{}, err
	}
}

// Recorder is the adapter of a channel with no provider yet. It writes nothing and sends
// nothing; the pipeline records the attempt, and the provider code on that attempt is what
// says the message never left this process.
type Recorder struct {
	providerCode string
	detail       string
	// undelivered is set for the channels that record an attempt and hand the message to
	// nobody. Empty for in-app, which is delivered by being written: the message log is
	// what the screen reads.
	undelivered string
	logger      *slog.Logger
}

// NewSMS returns the SMS stub. It exists so a message on the SMS channel is queued,
// rendered and logged today, and starts being delivered the day a provider is wired in
// without anything else changing.
func NewSMS(logger *slog.Logger) *Recorder {
	return newRecorder(ProviderSMS,
		"no SMS provider is configured; the attempt was recorded and nothing was sent",
		"NO_SMS_PROVIDER", logger)
}

// NewPush returns the push recorder. There is no device registry in this milestone.
func NewPush(logger *slog.Logger) *Recorder {
	return newRecorder(ProviderPush,
		"no push registry is configured; the attempt was recorded and nothing was sent",
		"NO_PUSH_REGISTRY", logger)
}

// NewInApp returns the in-app recorder. An in-app message is delivered by being stored:
// the message log is what a screen reads, so there is nothing else to hand it to.
func NewInApp(logger *slog.Logger) *Recorder {
	return newRecorder(ProviderInApp, "delivered by being recorded in the message log", "", logger)
}

func newRecorder(providerCode, detail, undelivered string, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{
		providerCode: providerCode, detail: detail, undelivered: undelivered, logger: logger,
	}
}

var _ application.ChannelSender = (*Recorder)(nil)

// Send implements application.ChannelSender. The log line carries ids and codes only: a
// rendered body in a log file is a notification in a place nobody applied a retention
// policy to.
func (r *Recorder) Send(_ context.Context, _ string, m application.MessageRecord) (
	application.SenderResult, error,
) {
	r.logger.Info("notification recorded without delivery",
		"provider_code", r.providerCode, "message_id", m.ID,
		"channel", m.Channel, "event_code", m.EventCode)
	return application.SenderResult{
		ProviderCode:      r.providerCode,
		ProviderMessageID: fmt.Sprintf("%s:%s", r.providerCode, m.ID),
		Outcome:           domain.OutcomeAccepted,
		Detail:            r.detail,
		UndeliveredReason: r.undelivered,
	}, nil
}
