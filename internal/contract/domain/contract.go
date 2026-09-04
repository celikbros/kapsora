// Package domain holds the contract rules: what a contract and its version may look like,
// the status machines both of them move through, and the shape of everything that hangs
// under a version — price lists, price items, packages, quotas and the payment term. It
// depends on nothing outside the standard library and the exact-decimal type of the
// benefit module, so every rule is unit-testable without a database.
//
// The money type is deliberately borrowed rather than reinvented: a price item and an
// entitlement balance are the same numeric(20,6) with the same "never a float" rule
// (handbook section 3), and two implementations of exact decimals in one system is one
// implementation too many.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
)

// Money is an exact numeric(20,6) value. It is the entitlement quantity type under
// another name, because a price and a balance are the same kind of number.
type Money = benefit.Quantity

// ParseMoney reads an exact decimal; it never goes through a float.
func ParseMoney(raw string) (Money, error) { return benefit.ParseQuantity(raw) }

// MaxScale is the number of decimal places numeric(20,6) keeps.
const MaxScale = benefit.MaxScale

// FieldError names one invalid request field; Field uses the JSON path of the contract.
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
	return fmt.Sprintf("contract: %d validation error(s)", len(e.Fields))
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

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("contract: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// ErrTransitionInvalid is raised when a contract status command asks for a move the
// machine does not allow.
var ErrTransitionInvalid = errors.New("contract: status transition not allowed")

// Contract statuses (migration 000021).
const (
	StatusDraft     = "DRAFT"
	StatusActive    = "ACTIVE"
	StatusSuspended = "SUSPENDED"
	StatusClosed    = "CLOSED"
)

// Contract version statuses (migration 000021).
const (
	VersionDraft       = "DRAFT"
	VersionUnderReview = "UNDER_REVIEW"
	VersionPublished   = "PUBLISHED"
	VersionRetired     = "RETIRED"
)

// Closed lists the database repeats as CHECK constraints.
var (
	ContractStatuses = []string{StatusDraft, StatusActive, StatusSuspended, StatusClosed}
	VersionStatuses  = []string{VersionDraft, VersionUnderReview, VersionPublished, VersionRetired}
	DomainCodes      = []string{
		"GENERIC", "HEALTH", "ACCOMMODATION", "ASSISTANCE", "EDUCATION",
		"SPORT", "TRANSPORT", "CARE", "OTHER",
	}
	UnitTypes      = []string{"MONEY", "COUNT", "NIGHT", "SESSION", "HOUR", "KILOMETER", "POINT"}
	PricingMethods = []string{"FIXED", "UNIT", "PERCENT_OF_LIST", "FORMULA"}
	// MemberShareMethods says how much of the price the member carries.
	MemberShareMethods = []string{"NONE", "FIXED", "PERCENT"}
	InclusionRules     = []string{"ALL", "ANY_OF_N"}
	QuotaPeriodTypes   = []string{"DAY", "WEEK", "MONTH", "YEAR", "CONTRACT"}
	SettlementMethods  = []string{"BANK_TRANSFER", "OFFSET", "OTHER"}
	TaxBehaviours      = []string{"EXCLUSIVE", "INCLUSIVE", "EXEMPT"}
)

// Pricing methods and member share methods by name, so the field rules below read as the
// CHECK constraints they mirror.
const (
	MethodFixed         = "FIXED"
	MethodUnit          = "UNIT"
	MethodPercentOfList = "PERCENT_OF_LIST"
	MethodFormula       = "FORMULA"

	ShareNone    = "NONE"
	ShareFixed   = "FIXED"
	SharePercent = "PERCENT"

	InclusionAll     = "ALL"
	InclusionAnyOfN  = "ANY_OF_N"
	TaxBehaviourNone = "EXEMPT"
)

// DefaultCurrency and DefaultPriority mirror the column defaults of migration 000021.
const (
	DefaultCurrency = "TRY"
	DefaultPriority = 100
	// MaxNotesLength and MaxCommentLength match the neighbouring modules.
	MaxNotesLength   = 2000
	MaxCommentLength = 1000
	// MaxPriority keeps a priority inside the int32 the column stores.
	MaxPriority = 10000
	// MaxDueDays is the ceiling the payment term CHECK repeats.
	MaxDueDays = 365
)

// contractTransitions is the whole status machine of a contract. A contract is activated
// when it starts being used, suspended and reactivated while it runs, and closed once, at
// which point nothing moves any more: the versions published under it stay readable
// exactly as they were, because claims were priced from them.
var contractTransitions = map[string][]string{
	StatusDraft:     {StatusActive, StatusClosed},
	StatusActive:    {StatusSuspended, StatusClosed},
	StatusSuspended: {StatusActive, StatusClosed},
}

// TransitionAllowed reports whether a contract may move from one status to another.
// Repeating the current status is allowed here, unlike the provider status commands: this
// is a merge-patch field, and a patch that leaves a field where it was is a no-op rather
// than a mistake.
func TransitionAllowed(from, to string) bool {
	if from == to {
		return true
	}
	for _, allowed := range contractTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition turns a refused move into the conflict the transport reports as
// CONTRACT_TRANSITION_INVALID.
func ValidateTransition(from, to string) error {
	if !TransitionAllowed(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionInvalid, from, to)
	}
	return nil
}

// Code, currency and text shapes; the database repeats every one of them as a CHECK.
var (
	codePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,39}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	formulaPattern  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,119}$`)
)

// Contains reports whether list holds v.
func Contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// DateOnly strips the clock so period comparisons stay day-based.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// DayPtr normalises an optional date to a day.
func DayPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := DateOnly(*t)
	return &d
}

// NewContract is the validated create command for a contract.
type NewContract struct {
	Code                  string
	Name                  string
	PayerOrganizationID   string
	ProviderProfileID     string
	SponsorOrganizationID string
	DomainCode            string
}

// ValidateNewContract checks the fields the database would otherwise reject with a raw
// constraint error.
func ValidateNewContract(in NewContract) error {
	ve := &ValidationError{}
	if !codePattern.MatchString(in.Code) {
		ve.Add("code", "FORMAT", "2-40 karakter, büyük harfle başlamalı, harf, rakam, _ ve - kullanılabilir")
	}
	validateName(ve, "name", in.Name)
	if in.PayerOrganizationID == "" {
		ve.Add("payerOrganizationId", "REQUIRED", "zorunlu alan")
	}
	if in.ProviderProfileID == "" {
		ve.Add("providerProfileId", "REQUIRED", "zorunlu alan")
	}
	if !Contains(DomainCodes, in.DomainCode) {
		ve.Add("domainCode", "ENUM", "geçersiz hizmet alanı")
	}
	return ve.OrNil()
}

// ContractPatch is a merge-patch of a contract; nil leaves a field unchanged and the Clear
// flag carries an explicit null.
type ContractPatch struct {
	Name                  *string
	SponsorOrganizationID *string
	ClearSponsor          bool
	Status                *string
	ExpectedVersion       int64
}

// ValidateContractPatch checks the fields a merge-patch may carry.
func ValidateContractPatch(p ContractPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, "name", *p.Name)
	}
	if p.Status != nil && !Contains(ContractStatuses, *p.Status) {
		ve.Add("status", "ENUM", "geçersiz sözleşme durumu")
	}
	return ve.OrNil()
}

// VersionInput is the validated period, currency and notes of a contract version; the
// same shape serves the create command and the merged result of a patch.
type VersionInput struct {
	ValidFrom    *time.Time
	ValidTo      *time.Time
	CurrencyCode string
	Notes        *string
}

// ValidateVersion checks a version's own fields. The period may still be open on a draft:
// it is submit that insists on a start, because only a published version has to be
// selectable by service date.
func ValidateVersion(in VersionInput) error {
	ve := &ValidationError{}
	if in.CurrencyCode != "" && !currencyPattern.MatchString(in.CurrencyCode) {
		ve.Add("currencyCode", "FORMAT", "ISO-4217 üç harfli kod olmalı")
	}
	if in.Notes != nil {
		validateText(ve, "notes", *in.Notes, MaxNotesLength)
	}
	if in.ValidFrom != nil && in.ValidTo != nil && !DateOnly(*in.ValidTo).After(DateOnly(*in.ValidFrom)) {
		ve.Add("validTo", "PERIOD", "bitiş tarihi başlangıçtan sonra olmalı")
	}
	return ve.OrNil()
}

// PriceListInput is one row of a price list set replacement.
type PriceListInput struct {
	Code        string
	Name        string
	Priority    int
	SeasonFrom  *time.Time
	SeasonTo    *time.Time
	WeekdayMask *int
}

// ValidatePriceLists checks the submitted set against itself. A PUT replaces the whole
// set, so a payload that names one code twice would never reach the unique index.
func ValidatePriceLists(items []PriceListInput) error {
	ve := &ValidationError{}
	seen := make(map[string]int, len(items))
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		if !codePattern.MatchString(row.Code) {
			ve.Add(path+".code", "FORMAT", "2-40 karakter, büyük harfle başlamalı, harf, rakam, _ ve - kullanılabilir")
		} else if first, dup := seen[row.Code]; dup {
			ve.Add(path+".code", "DUPLICATE",
				fmt.Sprintf("bu kod %d. satırda da kullanıldı", first))
		} else {
			seen[row.Code] = i
		}
		validateName(ve, path+".name", row.Name)
		validatePriority(ve, path+".priority", row.Priority)
		switch {
		case (row.SeasonFrom == nil) != (row.SeasonTo == nil):
			ve.Add(path+".seasonTo", "PAIR", "sezon başlangıcı ve bitişi birlikte verilmeli")
		case row.SeasonFrom != nil && !DateOnly(*row.SeasonTo).After(DateOnly(*row.SeasonFrom)):
			ve.Add(path+".seasonTo", "PERIOD", "sezon bitişi başlangıçtan sonra olmalı")
		}
		if row.WeekdayMask != nil && (*row.WeekdayMask < 1 || *row.WeekdayMask > 127) {
			ve.Add(path+".weekdayMask", "RANGE", "1 ile 127 arasında olmalı")
		}
	}
	return ve.OrNil()
}

// PriceItemInput is one row of a price item set replacement. The amounts arrive as the
// decimal strings the contract carries and are validated as exact decimals here; nothing
// in this package ever converts one to a float.
type PriceItemInput struct {
	ServiceDefinitionID string
	ServiceCategoryID   string
	PackageDefinitionID string
	LocationID          string
	UnitType            string
	PricingMethod       string
	Amount              string
	Percent             string
	FormulaKey          string
	MinAmount           string
	MaxAmount           string
	MemberShareMethod   string
	MemberShareAmount   string
	MemberSharePercent  string
	ValidFrom           time.Time
	ValidTo             *time.Time
	Priority            int
}

// ValidatePriceItems checks a submitted price item set. Every rule here is also a CHECK
// constraint on contract.price_item; stating them in Go too is what turns a raw
// constraint error into a field error a form can point at.
func ValidatePriceItems(items []PriceItemInput) error {
	ve := &ValidationError{}
	for i := range items {
		validatePriceItem(ve, fmt.Sprintf("items[%d]", i), items[i])
	}
	return ve.OrNil()
}

func validatePriceItem(ve *ValidationError, path string, row PriceItemInput) {
	targets := 0
	for _, id := range []string{row.ServiceDefinitionID, row.ServiceCategoryID, row.PackageDefinitionID} {
		if id != "" {
			targets++
		}
	}
	switch {
	case targets == 0:
		ve.Add(path+".serviceDefinitionId", "REQUIRED", "hizmet tanımı, kategori veya paket zorunlu")
	case targets > 1:
		ve.Add(path+".serviceDefinitionId", "EXCLUSIVE", "yalnızca biri verilebilir")
	}
	if !Contains(UnitTypes, row.UnitType) {
		ve.Add(path+".unitType", "ENUM", "geçersiz birim türü")
	}
	if !Contains(PricingMethods, row.PricingMethod) {
		ve.Add(path+".pricingMethod", "ENUM", "geçersiz fiyatlandırma yöntemi")
	}
	if row.MemberShareMethod != "" && !Contains(MemberShareMethods, row.MemberShareMethod) {
		ve.Add(path+".memberShareMethod", "ENUM", "geçersiz katılım payı yöntemi")
	}
	validateAmount(ve, path+".amount", row.Amount)
	validatePercent(ve, path+".percent", row.Percent)
	minAmount := validateAmount(ve, path+".minAmount", row.MinAmount)
	maxAmount := validateAmount(ve, path+".maxAmount", row.MaxAmount)
	validateAmount(ve, path+".memberShareAmount", row.MemberShareAmount)
	validatePercent(ve, path+".memberSharePercent", row.MemberSharePercent)

	// Each method needs its own field and forbids the others', so a row can never be half
	// specified in a way the calculator would have to guess about.
	switch row.PricingMethod {
	case MethodFixed, MethodUnit:
		requireField(ve, path+".amount", row.Amount)
		forbidField(ve, path+".percent", row.Percent)
		forbidField(ve, path+".formulaKey", row.FormulaKey)
	case MethodPercentOfList:
		requireField(ve, path+".percent", row.Percent)
		forbidField(ve, path+".amount", row.Amount)
		forbidField(ve, path+".formulaKey", row.FormulaKey)
	case MethodFormula:
		requireField(ve, path+".formulaKey", row.FormulaKey)
		forbidField(ve, path+".amount", row.Amount)
		forbidField(ve, path+".percent", row.Percent)
	}
	if row.FormulaKey != "" && !formulaPattern.MatchString(row.FormulaKey) {
		ve.Add(path+".formulaKey", "FORMAT", "1-120 karakter, büyük harf, rakam, _, . ve - kullanılabilir")
	}
	switch method(row.MemberShareMethod) {
	case ShareNone:
		forbidField(ve, path+".memberShareAmount", row.MemberShareAmount)
		forbidField(ve, path+".memberSharePercent", row.MemberSharePercent)
	case ShareFixed:
		requireField(ve, path+".memberShareAmount", row.MemberShareAmount)
		forbidField(ve, path+".memberSharePercent", row.MemberSharePercent)
	case SharePercent:
		requireField(ve, path+".memberSharePercent", row.MemberSharePercent)
		forbidField(ve, path+".memberShareAmount", row.MemberShareAmount)
	}
	if minAmount != nil && maxAmount != nil && maxAmount.Cmp(*minAmount) < 0 {
		ve.Add(path+".maxAmount", "RANGE", "üst sınır alt sınırdan küçük olamaz")
	}
	if row.ValidFrom.IsZero() {
		ve.Add(path+".validFrom", "REQUIRED", "zorunlu alan")
	}
	if row.ValidTo != nil && !row.ValidFrom.IsZero() && !DateOnly(*row.ValidTo).After(DateOnly(row.ValidFrom)) {
		ve.Add(path+".validTo", "PERIOD", "bitiş tarihi başlangıçtan sonra olmalı")
	}
	validatePriority(ve, path+".priority", row.Priority)
}

// method fills in the column default so an omitted member share behaves like NONE.
func method(v string) string {
	if v == "" {
		return ShareNone
	}
	return v
}

// PackageLineInput is one line of a package: a service definition and how much of it the
// bundle includes.
type PackageLineInput struct {
	ServiceDefinitionID string
	IncludedQuantity    string
}

// PackageInput is one package of a package set replacement.
type PackageInput struct {
	Code          string
	Name          string
	InclusionRule string
	MinLines      *int
	Lines         []PackageLineInput
}

// ValidatePackages checks a submitted package set against itself and against the CHECK
// constraints of contract.package_definition and contract.package_line.
func ValidatePackages(items []PackageInput) error {
	ve := &ValidationError{}
	seen := make(map[string]int, len(items))
	for i, pkg := range items {
		path := fmt.Sprintf("items[%d]", i)
		if !codePattern.MatchString(pkg.Code) {
			ve.Add(path+".code", "FORMAT", "2-40 karakter, büyük harfle başlamalı, harf, rakam, _ ve - kullanılabilir")
		} else if first, dup := seen[pkg.Code]; dup {
			ve.Add(path+".code", "DUPLICATE", fmt.Sprintf("bu kod %d. satırda da kullanıldı", first))
		} else {
			seen[pkg.Code] = i
		}
		validateName(ve, path+".name", pkg.Name)
		rule := pkg.InclusionRule
		if rule == "" {
			rule = InclusionAll
		}
		if !Contains(InclusionRules, rule) {
			ve.Add(path+".inclusionRule", "ENUM", "geçersiz kapsam kuralı")
		}
		switch {
		case rule == InclusionAnyOfN && pkg.MinLines == nil:
			ve.Add(path+".minLines", "REQUIRED", "ANY_OF_N için en az satır sayısı zorunlu")
		case rule == InclusionAll && pkg.MinLines != nil:
			ve.Add(path+".minLines", "FORBIDDEN", "ALL için en az satır sayısı verilemez")
		case pkg.MinLines != nil && *pkg.MinLines <= 0:
			ve.Add(path+".minLines", "RANGE", "sıfırdan büyük olmalı")
		case pkg.MinLines != nil && *pkg.MinLines > len(pkg.Lines):
			ve.Add(path+".minLines", "RANGE", "paket satır sayısından büyük olamaz")
		}
		if len(pkg.Lines) == 0 {
			ve.Add(path+".lines", "REQUIRED", "en az bir satır gerekli")
		}
		lineSeen := make(map[string]int, len(pkg.Lines))
		for j, line := range pkg.Lines {
			linePath := fmt.Sprintf("%s.lines[%d]", path, j)
			if line.ServiceDefinitionID == "" {
				ve.Add(linePath+".serviceDefinitionId", "REQUIRED", "zorunlu alan")
			} else if first, dup := lineSeen[line.ServiceDefinitionID]; dup {
				ve.Add(linePath+".serviceDefinitionId", "DUPLICATE",
					fmt.Sprintf("bu hizmet %d. satırda da var", first))
			} else {
				lineSeen[line.ServiceDefinitionID] = j
			}
			validatePositive(ve, linePath+".includedQuantity", line.IncludedQuantity)
		}
	}
	return ve.OrNil()
}

// QuotaInput is one row of a provider quota set replacement.
type QuotaInput struct {
	LocationID          string
	ServiceDefinitionID string
	PeriodType          string
	PeriodFrom          time.Time
	PeriodTo            time.Time
	Capacity            string
	AllowOverdraft      bool
}

// ValidateQuotas checks a submitted quota set. Two rows with the same scope and period
// would collide on uq_provider_quota_scope, which is NULLS NOT DISTINCT, so the duplicate
// check here treats an absent location or service as a value of its own.
func ValidateQuotas(items []QuotaInput) error {
	ve := &ValidationError{}
	seen := make(map[string]int, len(items))
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		if !Contains(QuotaPeriodTypes, row.PeriodType) {
			ve.Add(path+".periodType", "ENUM", "geçersiz dönem türü")
		}
		switch {
		case row.PeriodFrom.IsZero() || row.PeriodTo.IsZero():
			ve.Add(path+".periodTo", "REQUIRED", "dönem başlangıcı ve bitişi zorunlu")
		case !DateOnly(row.PeriodTo).After(DateOnly(row.PeriodFrom)):
			ve.Add(path+".periodTo", "PERIOD", "dönem bitişi başlangıçtan sonra olmalı")
		}
		validatePositive(ve, path+".capacity", row.Capacity)
		key := strings.Join([]string{
			row.LocationID, row.ServiceDefinitionID,
			DateOnly(row.PeriodFrom).Format(time.DateOnly), DateOnly(row.PeriodTo).Format(time.DateOnly),
		}, "|")
		if first, dup := seen[key]; dup {
			ve.Add(path+".periodFrom", "DUPLICATE",
				fmt.Sprintf("aynı kapsam ve dönem %d. satırda da var", first))
		} else {
			seen[key] = i
		}
	}
	return ve.OrNil()
}

// PaymentTermInput is the single payment term of a version.
type PaymentTermInput struct {
	DueDays          int
	SettlementMethod string
	TaxBehaviour     string
	VatRate          string
	LateFeePercent   string
}

// ValidatePaymentTerm checks the payment term. ck_payment_term_vat is the database's own
// statement of the same rule: anything but EXEMPT needs a VAT rate, because "how much tax"
// cannot be left open on an agreement somebody will invoice against.
func ValidatePaymentTerm(in PaymentTermInput) error {
	ve := &ValidationError{}
	if in.DueDays < 0 || in.DueDays > MaxDueDays {
		ve.Add("dueDays", "RANGE", fmt.Sprintf("0 ile %d arasında olmalı", MaxDueDays))
	}
	if !Contains(SettlementMethods, in.SettlementMethod) {
		ve.Add("settlementMethod", "ENUM", "geçersiz ödeme yöntemi")
	}
	if !Contains(TaxBehaviours, in.TaxBehaviour) {
		ve.Add("taxBehaviour", "ENUM", "geçersiz vergi davranışı")
	}
	validateRate(ve, "vatRate", in.VatRate)
	validateRate(ve, "lateFeePercent", in.LateFeePercent)
	if in.TaxBehaviour != TaxBehaviourNone && in.VatRate == "" {
		ve.Add("vatRate", "REQUIRED", "muaf olmayan sözleşmede KDV oranı zorunlu")
	}
	return ve.OrNil()
}

// ValidateReasonCode checks the reason a retire command carries.
func ValidateReasonCode(reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if strings.TrimSpace(reasonCode) == "" {
		ve.Add("reasonCode", "REQUIRED", "gerekçe kodu zorunlu")
	}
	if reasonText != nil {
		validateText(ve, "reasonText", *reasonText, MaxCommentLength)
	}
	return ve.OrNil()
}

// ValidateComment checks an optional review comment.
func ValidateComment(comment *string) error {
	ve := &ValidationError{}
	if comment != nil {
		validateText(ve, "comment", *comment, MaxCommentLength)
	}
	return ve.OrNil()
}

// likeEscaper makes the caller's own wildcards literal inside an ILIKE pattern.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// LikePattern wraps a trimmed search term in the contains-pattern the ILIKE filters
// expect. An empty term means "no filter".
func LikePattern(q string) string {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return ""
	}
	return "%" + likeEscaper.Replace(trimmed) + "%"
}

// ValidateSearchTerm checks a free-text filter before it reaches the database.
func ValidateSearchTerm(field, q string) error {
	if q == "" {
		return nil
	}
	if n := utf8.RuneCountInString(q); n < 2 || n > 120 {
		ve := &ValidationError{}
		ve.Add(field, "LENGTH", "2-120 karakter olmalı")
		return ve
	}
	return nil
}

// CanonicalDecimal returns the storage form of an exact decimal, or "" for an absent one.
// A value that does not parse is reported by the validators above long before this runs.
func CanonicalDecimal(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	q, err := ParseMoney(raw)
	if err != nil {
		return raw
	}
	return q.String()
}

func validateName(ve *ValidationError, field, name string) {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 2 || n > 200 {
		ve.Add(field, "LENGTH", "2-200 karakter olmalı")
	}
}

func validateText(ve *ValidationError, field, value string, maxLen int) {
	if utf8.RuneCountInString(value) > maxLen {
		ve.Add(field, "LENGTH", fmt.Sprintf("en fazla %d karakter olmalı", maxLen))
	}
}

func validatePriority(ve *ValidationError, field string, priority int) {
	if priority < 0 || priority > MaxPriority {
		ve.Add(field, "RANGE", fmt.Sprintf("0 ile %d arasında olmalı", MaxPriority))
	}
}

// validateAmount parses a non-negative exact decimal; an empty string means "absent" and
// returns nil without an error.
func validateAmount(ve *ValidationError, field, raw string) *Money {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	q, err := ParseMoney(raw)
	if err != nil {
		ve.Add(field, "FORMAT", "en fazla 6 ondalıklı kesin bir sayı olmalı")
		return nil
	}
	if q.IsNegative() {
		ve.Add(field, "RANGE", "negatif olamaz")
		return nil
	}
	return &q
}

// validatePositive requires a strictly positive exact decimal, which is what the
// included_quantity and capacity CHECK constraints demand.
func validatePositive(ve *ValidationError, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		ve.Add(field, "REQUIRED", "zorunlu alan")
		return
	}
	if q := validateAmount(ve, field, raw); q != nil && !q.IsPositive() {
		ve.Add(field, "RANGE", "sıfırdan büyük olmalı")
	}
}

// validatePercent is validateAmount with the 0..100 bound the column CHECK repeats.
func validatePercent(ve *ValidationError, field, raw string) *Money {
	q := validateAmount(ve, field, raw)
	if q == nil {
		return nil
	}
	hundred, err := ParseMoney("100")
	if err != nil {
		// Unreachable: "100" is a valid literal. Reported rather than panicked so a
		// future change to the decimal parser cannot take the process down.
		ve.Add(field, "FORMAT", "yüzde değeri doğrulanamadı")
		return nil
	}
	if q.Cmp(hundred) > 0 {
		ve.Add(field, "RANGE", "0 ile 100 arasında olmalı")
		return nil
	}
	return q
}

// validateRate is validatePercent for the numeric(5,2) rates of the payment term.
func validateRate(ve *ValidationError, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	before := ve.Len()
	q := validatePercent(ve, field, raw)
	if q == nil || ve.Len() > before {
		return
	}
	if _, frac, found := strings.Cut(q.String(), "."); found && len(frac) > 2 {
		ve.Add(field, "SCALE", "en fazla 2 ondalık basamak olabilir")
	}
}

func requireField(ve *ValidationError, field, value string) {
	if strings.TrimSpace(value) == "" {
		ve.Add(field, "REQUIRED", "bu yöntem için zorunlu alan")
	}
}

func forbidField(ve *ValidationError, field, value string) {
	if strings.TrimSpace(value) != "" {
		ve.Add(field, "FORBIDDEN", "bu yöntemle birlikte verilemez")
	}
}
