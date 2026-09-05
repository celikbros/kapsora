package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/notification/domain"
)

// The events the product actually tells somebody about (WP-I5-05 section 2.4). They are
// named here rather than at each publisher so an event code is written once: a template
// is published for a code, a preference is expressed about a code, and a message is found
// again by a code, and three spellings of the same thing would be three different events.
const (
	// EventServiceRequestDecided is a request that has been approved, partially approved,
	// rejected or sent back. All four are the same event with a different status word,
	// because to the member and the provider they are one thing: somebody has answered.
	EventServiceRequestDecided = "service_request.decided"
	// EventServiceRequestPendingDocument is the gate asking the provider for a document
	// before anybody can decide.
	EventServiceRequestPendingDocument = "service_request.pending_document"
	// EventAuthorizationApproved is the promise the member can now use.
	EventAuthorizationApproved = "authorization.approved"
	// EventAuthorizationExpiring is that promise running out, told before it does.
	EventAuthorizationExpiring = "authorization.expiring"
	// EventMedicalReportDecided and EventClaimDecided are raised by WP-I5-02 and WP-I5-04.
	// This package registers their templates and recipients so that the day those
	// commands publish, there is something to render and somebody to render it to.
	EventMedicalReportDecided = "medical_report.decided"
	EventClaimDecided         = "claim.decided"
)

// WiredEvents is every event this milestone publishes a template for, in a stable order.
// The seed walks it; a test walks it to prove each one refuses a diagnosis.
var WiredEvents = []string{
	EventServiceRequestDecided,
	EventServiceRequestPendingDocument,
	EventAuthorizationApproved,
	EventAuthorizationExpiring,
	EventMedicalReportDecided,
	EventClaimDecided,
}

// DefaultLocale is the one locale the seed publishes. A tenant serving another language
// publishes its own templates; nothing here assumes Turkish beyond the seed.
const DefaultLocale = "tr-TR"

// DefaultChannels is the pair every wired event goes out on.
//
// Both, and not one. INAPP is delivered by being written, so it always reaches the
// message log the screens read — which is what makes "was this person told" answerable
// even for somebody the platform holds no address for. EMAIL is the one that actually
// leaves the building. A recipient who wants neither turns them off per channel, which is
// what notification.preference is for.
var DefaultChannels = []string{domain.ChannelEmail, domain.ChannelInApp}

// Notification is one thing that happened, and who is to be told about it.
type Notification struct {
	EventCode string
	// Key names *what happened* — the request and the status it landed on, the
	// authorization and the day it expires — and never the moment the code ran. It is
	// what makes a redelivered command notify once, so it must be derivable again from
	// the same facts.
	Key        string
	Recipients []Recipient
	Variables  map[string]string
	// Channels defaults to DefaultChannels; Locale to DefaultLocale.
	Channels []string
	Locale   string
}

// PublishNotification writes one outbox row per recipient and channel, inside the
// caller's transaction. Nothing is rendered, read or sent here: a business command that
// rolls back has published nothing at all, and one that commits has published exactly
// these rows.
//
// A recipient with no id is skipped rather than refused. A request with no provider
// organization is an ordinary request, and refusing to decide it because there is nobody
// on the provider side to tell would be the notification tail wagging the decision dog.
func PublishNotification(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, n Notification) error {
	channels := n.Channels
	if len(channels) == 0 {
		channels = DefaultChannels
	}
	locale := n.Locale
	if locale == "" {
		locale = DefaultLocale
	}
	for _, recipient := range n.Recipients {
		if recipient.ID == uuid.Nil || recipient.Type == "" {
			continue
		}
		for _, channel := range channels {
			if err := Publish(ctx, tx, tenantID, Request{
				EventCode: n.EventCode, Recipient: recipient, Channel: channel,
				Locale: locale, Variables: n.Variables,
				DedupeKey: DedupeKey(n.EventCode, n.Key, recipient, channel),
			}); err != nil {
				return fmt.Errorf("notification: publish %s: %w", n.EventCode, err)
			}
		}
	}
	return nil
}

// DedupeKey is the one identity of "this event, to this recipient, on this channel". It
// is derived from facts rather than from a clock, so the same command replayed produces
// the same key and the message table refuses the second copy.
func DedupeKey(eventCode, key string, recipient Recipient, channel string) string {
	out := strings.Join([]string{
		eventCode, key, recipient.Type, recipient.ID.String(), channel,
	}, ":")
	if len(out) > domain.MaxDedupeKey {
		out = out[:domain.MaxDedupeKey]
	}
	return out
}

// PersonRecipient and OrganizationRecipient name the two sides of a request. They are
// helpers rather than constructors of anything, so a publisher cannot get the pair the
// wrong way round by writing the type as a string.
func PersonRecipient(id uuid.UUID) Recipient {
	return Recipient{Type: domain.RecipientPerson, ID: id}
}

// OrganizationRecipient names a provider organization.
func OrganizationRecipient(id uuid.UUID) Recipient {
	return Recipient{Type: domain.RecipientOrganization, ID: id}
}

// DeepLink builds the one link shape a notification may carry: a path into the product
// with no query string, so there is nowhere in it for a token to ride along.
func DeepLink(section string, id uuid.UUID) string {
	return "/" + section + "/" + id.String()
}

// SeedTemplate is one of the templates the product ships with (WP-I5-05 section 2.4).
//
// Every one of them is written from the safe-variable catalogue and nothing else: a
// reference, a status word, a date, an expiry, an amount and a link. There is no slot in
// any of them for a diagnosis, a reason text or an operator's comment, and that is not a
// matter of discipline — the renderer refuses a variable the template did not declare, and
// the catalogue has no name a clinical fact could be supplied under.
//
// Each event gets two: an EMAIL, which is the one that leaves the building, and an INAPP,
// which is delivered by being written and is therefore what makes "was this person told"
// answerable even for somebody the platform holds no address for.
//
// They live here rather than in cmd/seed so the refusal test can walk them: a template
// nobody could test is a template nobody has proved refuses anything.
type SeedTemplate struct {
	EventCode string
	Channel   string
	Subject   string
	Body      string
	Variables []string
}

// seedTemplatePair builds both channels of one event from one piece of Turkish, so the two
// versions of a message cannot drift apart into saying different things.
func seedTemplatePair(eventCode, subject, body string, variables ...string) []SeedTemplate {
	return []SeedTemplate{
		{EventCode: eventCode, Channel: domain.ChannelEmail, Subject: subject, Body: body, Variables: variables},
		{EventCode: eventCode, Channel: domain.ChannelInApp, Body: body, Variables: variables},
	}
}

// SeedTemplates is the whole set, in the order WiredEvents names them.
func SeedTemplates() []SeedTemplate {
	var out []SeedTemplate
	out = append(out, seedTemplatePair(
		EventServiceRequestDecided,
		"Talebiniz sonuçlandı: {{reference_no}}",
		"{{reference_no}} numaralı talebiniz {{event_date}} tarihinde sonuçlandı.\n"+
			"Sonuç: {{status_code}}\n\n"+
			"Ayrıntılar ve gerekçe için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventServiceRequestPendingDocument,
		"Belge bekleniyor: {{reference_no}}",
		"{{reference_no}} numaralı talep {{event_date}} tarihinde belge beklemeye alındı.\n"+
			"Durum: {{status_code}}\n\n"+
			"Hangi belgelerin gerektiğini görmek ve yüklemek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventAuthorizationApproved,
		"Ön onayınız hazır: {{reference_no}}",
		"{{reference_no}} numaralı ön onayınız {{event_date}} tarihinde verildi.\n"+
			"Durum: {{status_code}}\n"+
			"Son kullanma tarihi: {{expires_at}}\n\n"+
			"Ön onayı görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarExpiresAt, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventAuthorizationExpiring,
		"Ön onayınızın süresi doluyor: {{reference_no}}",
		"{{reference_no}} numaralı ön onayınızın süresi {{expires_at}} tarihinde doluyor.\n\n"+
			"Kullanmak ya da süre uzatımı istemek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarExpiresAt, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventMedicalReportDecided,
		"Raporunuz değerlendirildi: {{reference_no}}",
		"{{reference_no}} numaralı rapor {{event_date}} tarihinde değerlendirildi.\n"+
			"Sonuç: {{status_code}}\n\n"+
			"Değerlendirmeyi görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventClaimDecided,
		"Hasar dosyanız sonuçlandı: {{reference_no}}",
		"{{reference_no}} numaralı hasar dosyası {{event_date}} tarihinde sonuçlandı.\n"+
			"Sonuç: {{status_code}}\n"+
			"Tutar: {{amount}} {{currency}}\n\n"+
			"Ayrıntılar için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	return out
}
