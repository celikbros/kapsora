// Package domain holds what a notification may say and to whom: the closed lists the
// schema repeats as CHECK constraints, the catalogue of values a message is allowed to
// carry, the renderer that turns a template and a map into text, and the arithmetic of a
// quiet hours window. It depends on nothing outside the standard library, so every rule
// here is unit-testable without a database, an SMTP server or a clock.
//
// The rule the whole package is built around is stated once, here, as a pair of
// functions: ScreenVariables and Render. A message body is produced from a published
// template and a closed set of declared variables, a variable that is not declared is
// refused rather than dropped, and a value that does not have the shape its slot promises
// is refused too. There is deliberately no second place in the codebase that decides what
// may go into a notification.
//
// What a notification may carry: the person's given name, a reference number, a status
// word, a date, an amount with its currency, a provider or program name, and a link into
// the product. What it may not carry, ever: clinical detail, an identity number, or
// anything an operator typed into a comment. The catalogue below is that sentence written
// as code; migration 000029 writes the same sentence as a CHECK, so deleting one of them
// still leaves the other.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// FieldError names one invalid request field or variable; Field uses the JSON path of the
// contract, or the variable name when a render was refused.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError aggregates field errors for a 422 response.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("notification: %d validation error(s)", len(e.Fields))
}

// Add appends one field error.
func (e *ValidationError) Add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// Len reports how many field errors were collected.
func (e *ValidationError) Len() int { return len(e.Fields) }

// OrNil returns nil when nothing failed, so callers can `return ve.OrNil()`.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// Has reports whether any collected error carries the code, which is how a caller asks
// "was this refused because the value was unsafe" without matching on a message.
func (e *ValidationError) Has(code string) bool {
	for _, f := range e.Fields {
		if f.Code == code {
			return true
		}
	}
	return false
}

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("notification: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Field error codes a refused render produces. They are stable because the worker turns
// them into a permanent outbox failure and an operator reads them in the log.
const (
	// CodeUnknownVariable is a name that is not in the safe catalogue at all — a
	// diagnosis, an identity number, a comment. Nothing named this may ever be rendered.
	CodeUnknownVariable = "UNKNOWN_VARIABLE"
	// CodeNotDeclared is a catalogue name this template did not declare.
	CodeNotDeclared = "NOT_DECLARED"
	// CodeMissingVariable is a declared name the caller did not supply. It is refused
	// rather than rendered empty: half a sentence is worse than no message.
	CodeMissingVariable = "MISSING_VARIABLE"
	// CodeUnsafeValue is a catalogue name whose value does not have the shape its slot
	// promises: an identity number in the amount, a sentence in the given name.
	CodeUnsafeValue = "UNSAFE_VALUE"
)

// Channels (migration 000029). EMAIL is the only one with a subject line.
const (
	ChannelEmail = "EMAIL"
	ChannelSMS   = "SMS"
	ChannelPush  = "PUSH"
	ChannelInApp = "INAPP"
)

// Template statuses. A published template is immutable; a change is a new version.
const (
	TemplateDraft     = "DRAFT"
	TemplatePublished = "PUBLISHED"
	TemplateRetired   = "RETIRED"
)

// Message statuses.
const (
	MessageQueued     = "QUEUED"
	MessageSending    = "SENDING"
	MessageSent       = "SENT"
	MessageFailed     = "FAILED"
	MessageSuppressed = "SUPPRESSED"
)

// Suppression reasons. Every one of them is a row an operator can see: a member who was
// not told can be shown to have not been told, and why.
const (
	// SuppressedPreferenceDisabled is somebody who asked not to hear about this.
	SuppressedPreferenceDisabled = "PREFERENCE_DISABLED"
	// SuppressedQuietHours is the middle of their night.
	SuppressedQuietHours = "QUIET_HOURS"
	// SuppressedNoTemplate is an event nobody has written a message for yet.
	SuppressedNoTemplate = "NO_TEMPLATE"
	// SuppressedNotDeliverable is a channel that has no provider behind it yet: the
	// message was rendered and the attempt recorded, but nothing left the building. It is
	// a suppression rather than a send because the member was not told.
	SuppressedNotDeliverable = "CHANNEL_NOT_DELIVERABLE"
	// SuppressedNoAddress is a recipient the platform holds no address for on this
	// channel. It is a suppression rather than an error because it is a fact about the
	// recipient, not a failure of the send.
	SuppressedNoAddress = "NO_ADDRESS"
)

// Recipient types.
const (
	RecipientPerson       = "PERSON"
	RecipientActor        = "ACTOR"
	RecipientOrganization = "ORGANIZATION"
)

// Delivery outcomes.
const (
	// OutcomeAccepted is the provider taking responsibility for the message.
	OutcomeAccepted = "ACCEPTED"
	// OutcomeRejected is the provider refusing it for good. Retrying changes nothing, so
	// the send stops here.
	OutcomeRejected = "REJECTED"
	// OutcomeBounced is a message the provider accepted and could not deliver.
	OutcomeBounced = "BOUNCED"
	// OutcomeError is a provider that could not be reached or did not answer. It is the
	// one outcome worth trying again.
	OutcomeError = "ERROR"
)

// AggregateType is what a notification's own audit rows are recorded under.
const AggregateType = "NOTIFICATION"

// Closed lists the database repeats as CHECK constraints.
var (
	Channels           = []string{ChannelEmail, ChannelSMS, ChannelPush, ChannelInApp}
	TemplateStatuses   = []string{TemplateDraft, TemplatePublished, TemplateRetired}
	MessageStatuses    = []string{MessageQueued, MessageSending, MessageSent, MessageFailed, MessageSuppressed}
	RecipientTypes     = []string{RecipientPerson, RecipientActor, RecipientOrganization}
	DeliveryOutcomes   = []string{OutcomeAccepted, OutcomeRejected, OutcomeBounced, OutcomeError}
	SuppressionReasons = []string{
		SuppressedPreferenceDisabled, SuppressedQuietHours,
		SuppressedNoTemplate, SuppressedNoAddress, SuppressedNotDeliverable,
	}
)

// The safe variable catalogue. These twelve names are everything a notification may carry.
// Adding one costs a migration as well as an edit here, which is the right price for
// widening what may leave the system in an e-mail nobody can recall.
const (
	VarGivenName    = "given_name"
	VarReferenceNo  = "reference_no"
	VarStatusCode   = "status_code"
	VarEventDate    = "event_date"
	VarExpiresAt    = "expires_at"
	VarAmount       = "amount"
	VarCurrency     = "currency"
	VarProviderName = "provider_name"
	VarProgramName  = "program_name"
	// VarPropertyName is the hotel or guest house a booking is at (WP-I6-04, migration
	// 000039). It joined the catalogue rather than being folded into provider_name
	// because they are different things in the accommodation vertical: the provider is
	// the organization holding the contract and the property is the building the member
	// sleeps in, and one operator may run several. Its shape is provider_name's exactly --
	// a label, not a sentence.
	VarPropertyName = "property_name"
	// VarMaskedAccount is the last four characters of a bank account number (WP-I7-04,
	// migration 000046). It joined the catalogue because `reimbursement.paid` has to be
	// able to say which account the money went to, and folding four characters into
	// `reference_no` would have made one slot mean two things.
	//
	// Its rule is the narrowest here: exactly four upper-case alphanumerics, which is what
	// `billing.reimbursement.bank_account_masked` stores and what
	// `billing/domain.MaskAccount` produces. There is no shape of a whole IBAN, a name or a
	// sentence that fits in it, which is the point -- the catalogue is what makes "there is
	// no slot an account number could be supplied under" a fact rather than a habit.
	VarMaskedAccount = "masked_account"
	VarDeepLink      = "deep_link"
)

// SafeVariableNames is the catalogue in a stable order, for the API and for tests.
var SafeVariableNames = []string{
	VarGivenName, VarReferenceNo, VarStatusCode, VarEventDate, VarExpiresAt,
	VarAmount, VarCurrency, VarProviderName, VarProgramName, VarPropertyName,
	VarMaskedAccount, VarDeepLink,
}

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxSubject bounds a template's subject and a message's rendered subject.
	MaxSubject = 200
	// MaxBody bounds a template body.
	MaxBody = 5000
	// MaxRenderedBody bounds what a render may produce.
	MaxRenderedBody = 10000
	// MaxDeclaredVariables is the size of the catalogue: a template may declare each of
	// them at most once and nothing else. Migration 000046 repeats it as a CHECK, as
	// migration 000039 did before it.
	MaxDeclaredVariables = 12
	// MaxVariableValue is the longest value any slot accepts. A diagnosis sentence, a
	// document body and an operator's comment are all longer than this; a name, a
	// reference and a status word are all shorter.
	MaxVariableValue = 120
	// MaxGivenName is deliberately short. A given name is one to three words; a phrase
	// that needs more room is not a given name.
	MaxGivenName = 40
	// MaxGivenNameWords is the other half of that rule.
	MaxGivenNameWords = 3
	// MaxDisplayName bounds a provider or program name.
	MaxDisplayName = 80
	// MaxDisplayNameWords is the other half of that rule: a name is a label, not a
	// sentence, and a phrase long enough to describe something is not a name.
	MaxDisplayNameWords = 8
	// MaxDeepLinkSegments bounds a link into the product.
	MaxDeepLinkSegments = 8
	// MaxDedupeKey bounds notification.message.dedupe_key.
	MaxDedupeKey = 200
	// MaxProviderDetail bounds what a provider's answer may leave in the delivery row.
	MaxProviderDetail = 500
	// DigitRunLimit is the length of a digit run that is refused everywhere. It is the
	// length of the shortest identity number this rule exists to catch — a VKN has ten
	// digits and a TCKN eleven — and not one digit shorter, because a service request
	// reference is minted as SR-YYYYMMDD-XXXXXXXX and the date in the middle is eight.
	// At eight this rule refused the one value a notification exists to carry. Migration
	// 000030 repeats the same number as a CHECK.
	DigitRunLimit = 10
)

var (
	eventCodePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$`)
	localePattern     = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	timezonePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+/-]{0,59}$`)
	referencePattern  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._/-]{0,39}$`)
	statusCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)
	// maskedAccountPattern is `ck_billing_reimbursement_bank_mask`, repeated. Four
	// characters exactly: a rule that allowed five would be a rule that could one day allow
	// the whole number.
	maskedAccountPattern = regexp.MustCompile(`^[0-9A-Z]{4}$`)
	datePattern          = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	amountPattern        = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,6})?$`)
	currencyPattern      = regexp.MustCompile(`^[A-Z]{3}$`)
	// deepLinkPattern is a path and nothing else: no scheme, no host, no query string and
	// no fragment. That is what "no secret in a link" means here — there is nowhere in the
	// value to put a token, so a link can only ever point at a screen the recipient has to
	// sign in to see.
	deepLinkPattern = regexp.MustCompile(
		`^(/([a-z][a-z0-9-]{0,39}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}))+$`)
	// clockPattern is the wire and column form of a time of day, with the seconds a
	// `time` column answers optional and ignored.
	clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])(:[0-5][0-9])?$`)
	// placeholderPattern is the only substitution the renderer performs.
	placeholderPattern = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
	// uuidPattern is removed before the digit run check: a record id in a link is
	// indistinguishable from a run of digits to a regular expression, and refusing every
	// message whose link happened to contain eight digits would be a message nobody can
	// send for a reason nobody can explain. Migration 000029 strips the same shape.
	uuidPattern = regexp.MustCompile(
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
)

// ValidChannel reports whether c is one of the four.
func ValidChannel(c string) bool { return contains(Channels, c) }

// ValidRecipientType reports whether t is one of the three.
func ValidRecipientType(t string) bool { return contains(RecipientTypes, t) }

// ValidMessageStatus reports whether s is one of the five.
func ValidMessageStatus(s string) bool { return contains(MessageStatuses, s) }

// ValidOutcome reports whether o is one of the four.
func ValidOutcome(o string) bool { return contains(DeliveryOutcomes, o) }

// ValidEventCode reports whether code has the shape of an outbox event type.
func ValidEventCode(code string) bool { return eventCodePattern.MatchString(code) }

// ValidLocale reports whether locale is a language tag this product speaks.
func ValidLocale(locale string) bool { return localePattern.MatchString(locale) }

// SafeVariable reports whether name is in the catalogue.
func SafeVariable(name string) bool {
	_, ok := variableRules[name]
	return ok
}

// Template is one version of one message, as the renderer sees it.
type Template struct {
	EventCode         string
	Channel           string
	Locale            string
	VersionNo         int
	Subject           string
	Body              string
	DeclaredVariables []string
}

// Rendered is what a template and a set of variables produced.
type Rendered struct {
	Subject string
	Body    string
}

// hasDigitRun reports whether s contains DigitRunLimit or more digits in a row, which is
// the one shape a TCKN and a VKN both have and nothing a notification legitimately carries
// does. The limit is not a parameter on purpose: two callers with two limits would be two
// answers to the same question.
func hasDigitRun(s string) bool {
	run := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			run++
			if run >= DigitRunLimit {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// StripRecordIDs removes canonical uuids so a link into the product is not mistaken for
// an identity number. It is exported because the same removal happens in the schema, and
// a test that asserts the two agree needs to be able to call it.
func StripRecordIDs(s string) string { return uuidPattern.ReplaceAllString(s, " ") }

// variableRule is what one slot of the catalogue promises about its value. The message is
// Turkish because it reaches an operator through a 422.
type variableRule struct {
	code  string
	valid func(string) bool
}

// variableRules is the catalogue with its shapes. Every rule is narrow enough that an
// identity number, a diagnosis or a comment cannot satisfy it: a given name has no
// digits at all, a status is a code, a date is a date, and everything is bounded.
var variableRules = map[string]variableRule{
	VarGivenName: {
		code:  "ad, en fazla üç kelime ve rakamsız olmalı",
		valid: validGivenName,
	},
	VarReferenceNo: {
		code:  "referans numarası BÜYÜK harf, rakam ve . _ / - karakterlerinden oluşmalı",
		valid: func(v string) bool { return referencePattern.MatchString(v) },
	},
	VarStatusCode: {
		code:  "durum kodu BÜYÜK_HARF biçiminde olmalı",
		valid: func(v string) bool { return statusCodePattern.MatchString(v) },
	},
	VarEventDate: {
		code:  "tarih YYYY-AA-GG biçiminde olmalı",
		valid: validDate,
	},
	VarExpiresAt: {
		code:  "tarih YYYY-AA-GG biçiminde olmalı",
		valid: validDate,
	},
	VarAmount: {
		code:  "tutar ondalık sayı olmalı",
		valid: func(v string) bool { return amountPattern.MatchString(v) },
	},
	VarCurrency: {
		code:  "para birimi üç harfli ISO kodu olmalı",
		valid: func(v string) bool { return currencyPattern.MatchString(v) },
	},
	VarProviderName: {
		code:  "sağlayıcı adı kısa bir ad olmalı; cümle ya da liste taşıyamaz",
		valid: validDisplayName,
	},
	VarProgramName: {
		code:  "program adı kısa bir ad olmalı; cümle ya da liste taşıyamaz",
		valid: validDisplayName,
	},
	VarPropertyName: {
		code:  "tesis adı kısa bir ad olmalı; cümle ya da liste taşıyamaz",
		valid: validDisplayName,
	},
	VarMaskedAccount: {
		code:  "hesap maskesi tam olarak dört karakter olmalı",
		valid: func(v string) bool { return maskedAccountPattern.MatchString(v) },
	},
	VarDeepLink: {
		code:  "bağlantı yalnızca uygulama içi bir yol olmalı; sorgu dizesi taşıyamaz",
		valid: validDeepLink,
	},
}

func validGivenName(v string) bool {
	if v == "" || len([]rune(v)) > MaxGivenName {
		return false
	}
	words := 1
	for _, r := range v {
		switch {
		case unicode.IsLetter(r):
		case r == ' ':
			words++
		case r == '\'' || r == '’' || r == '-' || r == '.':
		default:
			// A digit, a comma, a slash, a newline: none of them belongs in a given name,
			// and every one of them is how something else would get in.
			return false
		}
	}
	return words <= MaxGivenNameWords
}

// validDisplayName is the shape of a provider or program name.
//
// It is worth being honest about what this rule can and cannot do. It refuses the
// punctuation a sentence needs — a comma, a semicolon, a colon — and it bounds the length
// and the word count, which is enough to refuse a real clinical phrase ("C50.9 malign meme
// neoplazmı, sağ üst dış kadran, evre 2A") and an operator's comment. It cannot tell a
// three word name from a three word phrase, and nothing that looks only at the value
// could. What actually keeps clinical detail out of a notification is the catalogue: there
// is no diagnosis slot, so putting one in a name slot is a caller deliberately mislabelling
// its own data, and the caller is our own code with its own review.
func validDisplayName(v string) bool {
	if v == "" || len([]rune(v)) > MaxDisplayName {
		return false
	}
	words := 1
	for _, r := range v {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == ' ':
			words++
		case strings.ContainsRune(".-'’&/()", r):
		default:
			// A comma, a semicolon, a colon, a newline: the punctuation of a sentence or a
			// list, none of which belongs in a name.
			return false
		}
	}
	return words <= MaxDisplayNameWords
}

func validDate(v string) bool {
	if !datePattern.MatchString(v) {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

func validDeepLink(v string) bool {
	if v == "" || len(v) > MaxVariableValue || !deepLinkPattern.MatchString(v) {
		return false
	}
	return strings.Count(v, "/") <= MaxDeepLinkSegments
}

// ScreenVariables checks a map against the catalogue alone, before any template is
// involved. It is the first thing every path does, so a value that may never leave the
// system is refused before it can be written anywhere — including into the safe_variables
// column of a message that was going to be suppressed anyway.
//
// Two rules, both of which have to be here rather than at the caller. A name that is not
// in the catalogue is refused: there is no slot for a diagnosis, so a diagnosis cannot be
// supplied under any name. And a value that does not have the shape its slot promises is
// refused: a slot exists for an amount, and an identity number is not an amount.
func ScreenVariables(vars map[string]string) error {
	ve := &ValidationError{}
	for name, value := range vars {
		rule, ok := variableRules[name]
		if !ok {
			ve.Add(name, CodeUnknownVariable,
				"bu değişken bildirimlerde taşınamaz")
			continue
		}
		if len([]rune(value)) > MaxVariableValue {
			ve.Add(name, CodeUnsafeValue,
				fmt.Sprintf("en fazla %d karakter olmalı", MaxVariableValue))
			continue
		}
		// The one rule every slot shares. A TCKN has eleven digits and a VKN has ten;
		// nothing a notification legitimately carries has eight in a row. `deep_link` is
		// checked with its record ids removed, because an id is not a digit run that
		// means anything to a reader.
		screened := value
		if name == VarDeepLink {
			screened = StripRecordIDs(value)
		}
		if hasDigitRun(screened) {
			ve.Add(name, CodeUnsafeValue,
				"kimlik numarası biçiminde bir değer taşınamaz")
			continue
		}
		if !rule.valid(value) {
			ve.Add(name, CodeUnsafeValue, rule.code)
		}
	}
	return ve.OrNil()
}

// Placeholders returns the variable names a piece of template text refers to, in the
// order they first appear.
func Placeholders(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ValidateTemplate checks everything about a template that can be checked without a
// database. The declared list and the placeholders the body actually uses must be the
// same set: a declared variable the body never mentions is a value every caller has to
// supply for nothing, and a placeholder nobody declared is a hole in the message.
func ValidateTemplate(t Template) error {
	ve := &ValidationError{}
	if !eventCodePattern.MatchString(t.EventCode) {
		ve.Add("eventCode", "FORMAT", "nokta ile ayrılmış küçük harf olay kodu olmalı")
	}
	if !ValidChannel(t.Channel) {
		ve.Add("channel", "ENUM", "geçerli bir kanal olmalı")
	}
	if !localePattern.MatchString(t.Locale) {
		ve.Add("locale", "FORMAT", "tr veya tr-TR biçiminde olmalı")
	}
	validateTemplateText(t, ve)
	declared := validateDeclaredVariables(t.DeclaredVariables, ve)
	if ve.Len() > 0 {
		return ve
	}

	used := map[string]bool{}
	for _, name := range append(Placeholders(t.Subject), Placeholders(t.Body)...) {
		used[name] = true
		if !declared[name] {
			ve.Add("declaredVariables", CodeNotDeclared,
				fmt.Sprintf("%s metinde kullanılıyor ama bildirilmemiş", name))
		}
	}
	for name := range declared {
		if !used[name] {
			ve.Add("declaredVariables", "UNUSED",
				fmt.Sprintf("%s bildirilmiş ama metinde kullanılmıyor", name))
		}
	}
	return ve.OrNil()
}

// validateTemplateText checks the two pieces of text a template carries: the subject only
// an e-mail has, and the body every channel has.
func validateTemplateText(t Template, ve *ValidationError) {
	subject := strings.TrimSpace(t.Subject)
	switch {
	case t.Channel == ChannelEmail && (subject == "" || len([]rune(subject)) > MaxSubject):
		ve.Add("subject", "LENGTH", fmt.Sprintf("e-posta konusu 1 ile %d karakter arasında olmalı", MaxSubject))
	case t.Channel != ChannelEmail && subject != "":
		ve.Add("subject", "UNSUPPORTED", "yalnızca e-posta şablonunda konu bulunur")
	}
	body := strings.TrimSpace(t.Body)
	if body == "" || len([]rune(body)) > MaxBody {
		ve.Add("body", "LENGTH", fmt.Sprintf("gövde 1 ile %d karakter arasında olmalı", MaxBody))
	}
	// A template is written by an operator rather than derived from anybody's record, so
	// it cannot carry one person's diagnosis — but it can carry an identity number or an
	// IBAN somebody pasted, and every recipient of the event would then receive it.
	if hasDigitRun(subject) {
		ve.Add("subject", CodeUnsafeValue, "kimlik numarası biçiminde bir değer taşınamaz")
	}
	if hasDigitRun(body) {
		ve.Add("body", CodeUnsafeValue, "kimlik numarası biçiminde bir değer taşınamaz")
	}
	// A `{{` the renderer would not substitute is a placeholder somebody meant and
	// mistyped, and it would reach the recipient verbatim.
	if strings.Contains(placeholderPattern.ReplaceAllString(t.Subject+"\n"+t.Body, ""), "{{") {
		ve.Add("body", "PLACEHOLDER", "eksik ya da hatalı {{degisken}} yer tutucusu var")
	}
}

// validateDeclaredVariables checks the list against the catalogue and returns it as a set.
func validateDeclaredVariables(names []string, ve *ValidationError) map[string]bool {
	declared := make(map[string]bool, len(names))
	if len(names) > MaxDeclaredVariables {
		ve.Add("declaredVariables", "LENGTH",
			fmt.Sprintf("en fazla %d değişken bildirilebilir", MaxDeclaredVariables))
	}
	for _, name := range names {
		if !SafeVariable(name) {
			ve.Add("declaredVariables", CodeUnknownVariable,
				fmt.Sprintf("%s bildirimlerde taşınamaz", name))
			continue
		}
		if declared[name] {
			ve.Add("declaredVariables", "DUPLICATE", fmt.Sprintf("%s iki kez bildirilmiş", name))
			continue
		}
		declared[name] = true
	}
	return declared
}

// Render produces the text of one message. It refuses, and produces nothing, when the
// variables are not exactly the ones the template declared or when any of them carries
// something a notification may not.
//
// linkBase is prefixed to the deep_link path. The path never carries a query string, so
// there is nowhere for a token to ride along: a link points at a screen the recipient has
// to sign in to see, and a voucher's plaintext is never one of these values.
func Render(t Template, vars map[string]string, linkBase string) (Rendered, error) {
	if err := ScreenVariables(vars); err != nil {
		return Rendered{}, err
	}

	ve := &ValidationError{}
	declared := make(map[string]bool, len(t.DeclaredVariables))
	for _, name := range t.DeclaredVariables {
		declared[name] = true
		if _, ok := vars[name]; !ok {
			ve.Add(name, CodeMissingVariable, "şablonun beklediği değişken gönderilmedi")
		}
	}
	for name := range vars {
		if !declared[name] {
			// The rule the package exists for: an undeclared variable is refused rather
			// than dropped. Dropping it would render a body that quietly says something
			// other than what the caller meant.
			ve.Add(name, CodeNotDeclared, "bu şablon bu değişkeni bildirmiyor")
		}
	}
	if ve.Len() > 0 {
		return Rendered{}, ve
	}

	values := make(map[string]string, len(vars))
	for name, value := range vars {
		if name == VarDeepLink {
			values[name] = linkBase + value
			continue
		}
		values[name] = value
	}

	out := Rendered{
		Subject: substitute(t.Subject, values),
		Body:    substitute(t.Body, values),
	}
	if len([]rune(out.Subject)) > MaxSubject {
		ve.Add("subject", "LENGTH", fmt.Sprintf("üretilen konu %d karakteri aşıyor", MaxSubject))
	}
	if strings.TrimSpace(out.Body) == "" || len([]rune(out.Body)) > MaxRenderedBody {
		ve.Add("body", "LENGTH", fmt.Sprintf("üretilen gövde 1 ile %d karakter arasında olmalı", MaxRenderedBody))
	}
	// The last check, on the text that actually leaves the system. Every value was
	// screened on its own; this catches what two of them make when they meet.
	if hasDigitRun(StripRecordIDs(out.Subject)) || hasDigitRun(StripRecordIDs(out.Body)) {
		ve.Add("body", CodeUnsafeValue, "üretilen metin kimlik numarası biçiminde bir değer içeriyor")
	}
	if ve.Len() > 0 {
		return Rendered{}, ve
	}
	return out, nil
}

// substitute replaces every placeholder with its value. A name with no value cannot reach
// here: Render has already refused the render.
func substitute(text string, values map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		return values[name]
	})
}

// ClockTime is a time of day as minutes since midnight, which is what a quiet hours
// window is: a pair of wall clock times in somebody's own zone, with no date attached.
type ClockTime int

// MinutesPerDay is the modulus a quiet hours window wraps around.
const MinutesPerDay = 24 * 60

// NewClockTime builds a time of day from an hour and a minute.
func NewClockTime(hour, minute int) ClockTime {
	return ClockTime(((hour*60+minute)%MinutesPerDay + MinutesPerDay) % MinutesPerDay)
}

// Valid reports whether c is inside a day.
func (c ClockTime) Valid() bool { return c >= 0 && c < MinutesPerDay }

// String renders HH:MM, which is what the contract carries.
func (c ClockTime) String() string { return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60) }

// ParseClockTime reads an HH:MM or HH:MM:SS time of day. Seconds are accepted because the
// database column is a `time` and answers one; they are dropped, because a quiet hours
// window that ends at 07:59:59 is a window somebody meant to end at 08:00.
func ParseClockTime(raw string) (ClockTime, error) {
	m := clockPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0, fmt.Errorf("notification: %q is not an HH:MM time of day", raw)
	}
	hour, minute := int(m[1][0]-'0')*10+int(m[1][1]-'0'), int(m[2][0]-'0')*10+int(m[2][1]-'0')
	return NewClockTime(hour, minute), nil
}

// ValidTimezone reports whether tz is a zone name this process can actually resolve. The
// zone database is embedded (see tzdata.go), so the answer does not depend on what the
// host happens to have installed.
func ValidTimezone(tz string) bool {
	if !timezonePattern.MatchString(tz) {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// InQuietHours reports whether now falls inside [start, end) read in tz. A window that
// wraps midnight — 22:00 to 08:00, which is the one people actually set — is the normal
// case rather than the exception, so it is handled here rather than by whoever calls.
//
// An unresolvable zone is an error rather than a false: answering "not in quiet hours"
// would send a message in the middle of somebody's night because a zone name was
// misspelt, and answering "in quiet hours" would silently suppress everything.
func InQuietHours(now time.Time, tz string, start, end ClockTime) (bool, error) {
	if !start.Valid() || !end.Valid() {
		return false, fmt.Errorf("notification: quiet hours %d-%d are outside a day", start, end)
	}
	if start == end {
		return false, errors.New("notification: quiet hours that start and end together mean nothing")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false, fmt.Errorf("notification: unknown time zone %q: %w", tz, err)
	}
	local := now.In(loc)
	minute := ClockTime(local.Hour()*60 + local.Minute())
	if start < end {
		return minute >= start && minute < end, nil
	}
	return minute >= start || minute < end, nil
}

// ValidateDedupeKey checks the key that makes one event notify once.
func ValidateDedupeKey(key string) error {
	if key == "" || len(key) > MaxDedupeKey {
		ve := &ValidationError{}
		ve.Add("dedupeKey", "LENGTH", fmt.Sprintf("1 ile %d karakter arasında olmalı", MaxDedupeKey))
		return ve
	}
	return nil
}

// ValidatePreference checks one preference row before it is written.
func ValidatePreference(recipientType, eventCode, channel, timezone string,
	quietStart, quietEnd *ClockTime,
) error {
	ve := &ValidationError{}
	if !ValidRecipientType(recipientType) {
		ve.Add("recipientType", "ENUM", "geçerli bir alıcı türü olmalı")
	}
	if eventCode != "" && !eventCodePattern.MatchString(eventCode) {
		ve.Add("eventCode", "FORMAT", "nokta ile ayrılmış küçük harf olay kodu olmalı")
	}
	if !ValidChannel(channel) {
		ve.Add("channel", "ENUM", "geçerli bir kanal olmalı")
	}
	if !ValidTimezone(timezone) {
		ve.Add("timezone", "FORMAT", "geçerli bir IANA saat dilimi olmalı")
	}
	switch {
	case (quietStart == nil) != (quietEnd == nil):
		ve.Add("quietHoursStart", "REQUIRED", "sessiz saatler başlangıç ve bitişiyle birlikte verilir")
	case quietStart != nil && *quietStart == *quietEnd:
		ve.Add("quietHoursStart", "RANGE", "sessiz saatlerin başlangıcı ve bitişi aynı olamaz")
	case quietStart != nil && (!quietStart.Valid() || !quietEnd.Valid()):
		ve.Add("quietHoursStart", "RANGE", "saat 00:00 ile 23:59 arasında olmalı")
	}
	return ve.OrNil()
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
