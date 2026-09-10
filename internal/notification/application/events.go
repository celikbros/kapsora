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

// The accommodation events (WP-I6-04 section 2.5). They are named here, with the other
// six, because an event code is written once: WP-I6-02 and WP-I6-03 publish them, the seed
// registers a template for them, a member expresses a preference about them, and three
// spellings would be three different events.
//
// Who each one is told to is stated in BookingRecipients below rather than left to each
// publisher, because "does the hotel hear about this" is a privacy decision and not a
// detail of whichever command happens to raise it.
const (
	// EventBookingHeld is a room set aside with a countdown running. The member is told,
	// so the countdown is not a secret only the browser tab that opened it knows about.
	EventBookingHeld = "booking.held"
	// EventBookingConfirmed is the stay agreed. Both sides are told: the member, and the
	// property that has to expect them. The message carries the reference, the dates, the
	// property, the member's own share and a link - and never the voucher token, which has
	// no slot in the catalogue it could be supplied under.
	EventBookingConfirmed = "booking.confirmed"
	// EventBookingPendingApproval is a booking waiting on a step-up or a second pair of
	// eyes. Only the member is told: the property has not been promised anything yet.
	EventBookingPendingApproval = "booking.pending_approval"
	// EventBookingCancelled is a stay called off, with what it cost. Both sides are told,
	// because the property loses a room and the member owes a fee, and neither should
	// first learn it from an invoice.
	EventBookingCancelled = "booking.cancelled"
	// EventBookingReminder is the day before check-in, raised by the scheduler in the
	// property's own zone. Only the member is told, and only once: the dedupe key is
	// derived from the booking and the day rather than from a clock, so a job that runs
	// twice sends once.
	EventBookingReminder = "booking.reminder"
	// EventBookingNoShowReported is a member who did not arrive, with what the contract
	// says that costs. Only the member is told; the property is the one who reported it.
	EventBookingNoShowReported = "booking.no_show_reported"
	// EventBookingOffered is a waitlisted member reaching the front of the queue, with the
	// day the offer runs out.
	EventBookingOffered = "booking.offered"
)

// The icmal events (WP-I7-03 section 2.2 and 2.3). They are named here, with the rest, for the
// reason the accommodation ones are: an event code is written once, and three spellings of the
// same thing would be three different events.
//
// Who each one goes to is not a table here, because it is not a choice: `batch.submitted` is
// told to the payer organization whose finance department has to review it, and `batch.decided`
// to the provider organization that sent it. Neither ever reaches a member — an icmal is a
// conversation between two organizations about money, and no person is a party to it.
const (
	// EventBatchSubmitted is a provider's icmal arriving in the payer's finance queue, with
	// what it is worth and how many documents it covers.
	EventBatchSubmitted = "batch.submitted"
	// EventBatchDecided is the answer coming back: the totals, and a link to the returns and
	// their reasons. The reasons themselves are on the screen the link leads to, because a
	// reviewer's free text has no slot in the safe-variable catalogue and never will.
	EventBatchDecided = "batch.decided"
)

// The settlement, the payment and the member's reimbursement (WP-I7-04 section 2.5). They are
// named here, with the rest, for the reason every other block is: an event code is written
// once.
//
// Two of them reach an organization and two reach a person, and that split is the privacy
// decision of this package rather than a detail of whichever command raises them. A provider
// hears what it is owed and what has been paid; a member hears what was decided about their own
// receipt and when the money went. Neither ever hears about the other's.
const (
	// EventSettlementApproved is what the payer owes, released: the reference, what will
	// actually arrive, and the day it is due.
	EventSettlementApproved = "settlement.approved"
	// EventPaymentRecorded is a transfer somebody outside this system made, entered against a
	// settlement. It carries the amount and the settlement's reference and deliberately not the
	// bank's own transaction identifier -- that is behind a sign-in, where a value somebody
	// could quote back as proof of legitimacy belongs.
	EventPaymentRecorded = "payment.recorded"
	// EventReimbursementDecided is the member's receipt answered: approved in full, in part or
	// refused. It carries no bank detail at all.
	EventReimbursementDecided = "reimbursement.decided"
	// EventReimbursementPaid is the money actually gone, with the four characters of the
	// account it went to. It is the only message in this product that carries anything about a
	// bank account, and four characters is all it can carry.
	EventReimbursementPaid = "reimbursement.paid"
)

// RecipientKind names a side of a booking without naming a row: who is told, decided once
// per event rather than at each publisher.
type RecipientKind string

const (
	// RecipientMember is the person the booking is for.
	RecipientMember RecipientKind = "MEMBER"
	// RecipientProperty is the provider organization that runs the property.
	RecipientProperty RecipientKind = "PROPERTY"
)

// BookingRecipients is who hears about each accommodation event. It is a table rather than
// a rule restated in each command, because the answer is a privacy decision: the property
// learns that a member is coming and that they cancelled, and learns nothing about an
// approval still pending, a reminder, a no-show it reported itself, or an offer made to
// somebody on a waiting list.
var BookingRecipients = map[string][]RecipientKind{
	EventBookingHeld:            {RecipientMember},
	EventBookingConfirmed:       {RecipientMember, RecipientProperty},
	EventBookingPendingApproval: {RecipientMember},
	EventBookingCancelled:       {RecipientMember, RecipientProperty},
	EventBookingReminder:        {RecipientMember},
	EventBookingNoShowReported:  {RecipientMember},
	EventBookingOffered:         {RecipientMember},
}

// WiredEvents is every event this milestone publishes a template for, in a stable order.
// The seed walks it; a test walks it to prove each one refuses a diagnosis.
var WiredEvents = []string{
	EventServiceRequestDecided,
	EventServiceRequestPendingDocument,
	EventAuthorizationApproved,
	EventAuthorizationExpiring,
	EventMedicalReportDecided,
	EventClaimDecided,
	EventBookingHeld,
	EventBookingConfirmed,
	EventBookingPendingApproval,
	EventBookingCancelled,
	EventBookingReminder,
	EventBookingNoShowReported,
	EventBookingOffered,
	EventBatchSubmitted,
	EventBatchDecided,
	EventSettlementApproved,
	EventPaymentRecorded,
	EventReimbursementDecided,
	EventReimbursementPaid,
}

// BookingEvents is the accommodation subset, in the same order. WP-I6-02 and WP-I6-03 walk
// it; a test walks it to prove every one has a recipient and a pair of templates.
var BookingEvents = []string{
	EventBookingHeld,
	EventBookingConfirmed,
	EventBookingPendingApproval,
	EventBookingCancelled,
	EventBookingReminder,
	EventBookingNoShowReported,
	EventBookingOffered,
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
	out = append(out, bookingSeedTemplates()...)
	out = append(out, batchSeedTemplates()...)
	out = append(out, settlementSeedTemplates()...)
	return out
}

// settlementSeedTemplates is WP-I7-04's half of the catalogue: what the payer owes, what has
// been paid, and the two messages the member gets about their own receipt.
//
// The one worth reading twice is `reimbursement.paid`. It carries `masked_account`, which is
// four characters, and that is every word this product will ever say to anybody about a bank
// account. The IBAN the member typed exists as one ciphertext in one column; it is not in this
// template, not in the variables the command supplies, not in the rendered body and not in the
// outbox payload beside it -- and the catalogue is what makes that a fact about the schema
// rather than a promise about this file.
//
// What is deliberately not in `payment.recorded` is the bank's own transaction reference. It is
// the kind of value that turns up in a phishing message quoted back at somebody as proof that
// the sender is genuine; the settlement's reference is what a provider needs to find the row,
// and the bank reference is on the screen behind a sign-in.
func settlementSeedTemplates() []SeedTemplate {
	var out []SeedTemplate
	out = append(out, seedTemplatePair(
		EventSettlementApproved,
		"Ödeme mutabakatınız onaylandı: {{reference_no}}",
		"{{reference_no}} numaralı mutabakat {{event_date}} tarihinde onaylandı.\n"+
			"Durum: {{status_code}}\n"+
			"Ödenecek tutar: {{amount}} {{currency}}\n"+
			"Son ödeme tarihi: {{expires_at}}\n\n"+
			"Kesintiler ve ayrıntılar için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarExpiresAt, domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventPaymentRecorded,
		"Mutabakatınıza ödeme kaydedildi: {{reference_no}}",
		"{{reference_no}} numaralı mutabakat için {{event_date}} tarihli bir ödeme kaydedildi.\n"+
			"Kaydedilen tutar: {{amount}} {{currency}}\n"+
			"Mutabakatın durumu: {{status_code}}\n\n"+
			"Ödeme kayıtlarını ve banka referansını görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventReimbursementDecided,
		"Geri ödeme başvurunuz sonuçlandı: {{reference_no}}",
		"{{reference_no}} numaralı geri ödeme başvurunuz {{event_date}} tarihinde sonuçlandı.\n"+
			"Sonuç: {{status_code}}\n"+
			"Onaylanan tutar: {{amount}} {{currency}}\n\n"+
			"Gerekçeyi ve ayrıntıları görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventReimbursementPaid,
		"Geri ödemeniz gönderildi: {{reference_no}}",
		"{{reference_no}} numaralı geri ödemeniz {{event_date}} tarihinde gönderildi.\n"+
			"Tutar: {{amount}} {{currency}}\n"+
			"Hesabınızın son dört hanesi: {{masked_account}}\n"+
			"Durum: {{status_code}}\n\n"+
			"Ayrıntılar için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarMaskedAccount, domain.VarDeepLink,
	)...)
	return out
}

// batchSeedTemplates is the icmal half of the catalogue (WP-I7-03).
//
// Both messages are written from the safe-variable catalogue and nothing else: a reference, a
// status word, a day, an amount, a currency and a link. What is deliberately not in either of
// them is the reason a reviewer typed and the number of any invoice inside the batch -- the
// first is free prose and the second is a provider's fiscal document number, and neither is
// something to put in an e-mail that leaves the building. Both are on the screen the deep link
// leads to, behind a sign-in, which is where they belong.
func batchSeedTemplates() []SeedTemplate {
	var out []SeedTemplate
	out = append(out, seedTemplatePair(
		EventBatchSubmitted,
		"Yeni icmal incelemenizi bekliyor: {{reference_no}}",
		"{{provider_name}} {{event_date}} tarihinde {{reference_no}} numaralı icmali gönderdi.\n"+
			"Toplam tutar: {{amount}} {{currency}}\n\n"+
			"Faturaları tek tek incelemek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarProviderName, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBatchDecided,
		"İcmaliniz sonuçlandı: {{reference_no}}",
		"{{reference_no}} numaralı icmal {{event_date}} tarihinde sonuçlandı.\n"+
			"Durum: {{status_code}}\n"+
			"Onaylanan tutar: {{amount}} {{currency}}\n\n"+
			"Fatura bazında kararları ve iade gerekçelerini görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarStatusCode, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	return out
}

// bookingSeedTemplates is the accommodation half of the catalogue (WP-I6-04 section 2.5).
//
// Two things are worth stating about what is deliberately not in them. There is no voucher
// token and no room number, because the catalogue has no slot either could be supplied
// under: a booking's proof of entitlement is fetched from behind a sign-in through the deep
// link, and a message that carried it would be a message that opens a door.
//
// And the two time-bounded messages - the hold's countdown and the waitlist offer's expiry
// - name a day rather than a minute. `expires_at` is a date in this catalogue, and the rule
// that refuses anything else is what stops a free-text time being smuggled in as one. The
// exact minute is on the screen the link leads to, which is where a countdown belongs
// anyway: one printed into an e-mail is already wrong by the time it is read.
func bookingSeedTemplates() []SeedTemplate {
	var out []SeedTemplate
	out = append(out, seedTemplatePair(
		EventBookingHeld,
		"Rezervasyon seçiminiz ayrıldı: {{reference_no}}",
		"{{property_name}} için {{reference_no}} numaralı seçiminiz sizin adınıza ayrıldı.\n"+
			"Onay için son gün: {{expires_at}}\n\n"+
			"Kalan süreyi görmek ve onaylamak için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarExpiresAt, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingConfirmed,
		"Rezervasyonunuz onaylandı: {{reference_no}}",
		"{{property_name}} rezervasyonunuz onaylandı.\n"+
			"Referans: {{reference_no}}\n"+
			"Giriş: {{event_date}}  Çıkış: {{expires_at}}\n"+
			"Ödeyeceğiniz tutar: {{amount}} {{currency}}\n\n"+
			"Rezervasyon belgeniz ve ayrıntılar için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarEventDate, domain.VarExpiresAt,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingPendingApproval,
		"Rezervasyonunuz onay bekliyor: {{reference_no}}",
		"{{property_name}} için {{reference_no}} numaralı rezervasyonunuz onay bekliyor.\n"+
			"Giriş: {{event_date}}\n"+
			"Durum: {{status_code}}\n\n"+
			"Durumu izlemek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarEventDate,
		domain.VarStatusCode, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingCancelled,
		"Rezervasyonunuz iptal edildi: {{reference_no}}",
		"{{property_name}} için {{reference_no}} numaralı rezervasyon iptal edildi.\n"+
			"Giriş tarihi: {{event_date}}\n"+
			"Durum: {{status_code}}\n"+
			"İptal bedeli: {{amount}} {{currency}}\n\n"+
			"İptal koşulları ve ayrıntılar için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarEventDate,
		domain.VarStatusCode, domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingReminder,
		"Yarın girişiniz var: {{reference_no}}",
		"{{property_name}} rezervasyonunuzun girişi {{event_date}} tarihinde.\n"+
			"Referans: {{reference_no}}\n\n"+
			"Giriş bilgileri için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarEventDate, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingNoShowReported,
		"Girişiniz yapılmadı: {{reference_no}}",
		"{{property_name}} için {{reference_no}} numaralı rezervasyonda {{event_date}} tarihinde giriş yapılmadı olarak bildirildi.\n"+
			"Sözleşme gereği tahakkuk eden tutar: {{amount}} {{currency}}\n\n"+
			"İtiraz etmek ya da ayrıntıları görmek için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarEventDate,
		domain.VarAmount, domain.VarCurrency, domain.VarDeepLink,
	)...)
	out = append(out, seedTemplatePair(
		EventBookingOffered,
		"Bekleme listenizde yer açıldı: {{reference_no}}",
		"{{property_name}} için beklediğiniz tarihlerde yer açıldı.\n"+
			"Referans: {{reference_no}}\n"+
			"Teklifin son günü: {{expires_at}}\n\n"+
			"Teklifi görmek ve kullanmak için: {{deep_link}}",
		domain.VarReferenceNo, domain.VarPropertyName, domain.VarExpiresAt, domain.VarDeepLink,
	)...)
	return out
}
