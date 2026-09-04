package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ConfigurationVersion is the version tag of the canonical form below. It is part of the
// hashed document, so a future change to the serialisation cannot silently produce the
// same hash for a different reading of the same price sheet.
const ConfigurationVersion = 1

// PriceContent is everything a published contract version froze: its period and currency,
// its price lists with their items, its packages, its quotas and its payment term. It is
// assembled by the application layer from the stored rows and hashed here.
type PriceContent struct {
	ValidFrom    *time.Time
	ValidTo      *time.Time
	CurrencyCode string
	PriceLists   []PriceListContent
	Packages     []PackageContent
	Quotas       []QuotaContent
	PaymentTerm  *PaymentTermInput
}

// PriceListContent is one price list with the items under it.
type PriceListContent struct {
	Code        string
	Name        string
	Priority    int
	SeasonFrom  *time.Time
	SeasonTo    *time.Time
	WeekdayMask *int
	Items       []PriceItemContent
}

// PriceItemContent is one price item as the hash sees it. The three targets are named by
// their catalog code rather than by their id, so copying a version into a new one and
// publishing it produces the same hash for the same agreed prices.
type PriceItemContent struct {
	DefinitionCode     string
	CategoryCode       string
	PackageCode        string
	LocationID         string
	UnitType           string
	PricingMethod      string
	Amount             string
	Percent            string
	FormulaKey         string
	MinAmount          string
	MaxAmount          string
	MemberShareMethod  string
	MemberShareAmount  string
	MemberSharePercent string
	ValidFrom          time.Time
	ValidTo            *time.Time
	Priority           int
}

// PackageContent is one package with its lines.
type PackageContent struct {
	Code          string
	Name          string
	InclusionRule string
	MinLines      *int
	Lines         []PackageLineContent
}

// PackageLineContent is one line of a package.
type PackageLineContent struct {
	DefinitionCode   string
	IncludedQuantity string
}

// QuotaContent is one provider quota. The consumed counter is deliberately absent:
// authorization moves it after publication, and a hash that changed when a quota was used
// would stop proving what was agreed.
type QuotaContent struct {
	LocationID     string
	DefinitionCode string
	PeriodType     string
	PeriodFrom     time.Time
	PeriodTo       time.Time
	Capacity       string
	AllowOverdraft bool
}

// CanonicalConfiguration returns the byte string a contract version's configuration hash
// is taken over:
//
//   - object keys are sorted (encoding/json sorts map keys),
//   - price lists, items, packages, lines and quotas are sorted by their own key,
//   - every numeric value is an exact decimal string with trailing zeros trimmed,
//   - dates are ISO days and absent values are null.
//
// Two readings of the same stored version therefore produce identical bytes, whatever
// order the rows arrived in.
func CanonicalConfiguration(c PriceContent) ([]byte, error) {
	lists := append([]PriceListContent(nil), c.PriceLists...)
	sort.Slice(lists, func(i, j int) bool { return lists[i].Code < lists[j].Code })
	listDocs := make([]any, 0, len(lists))
	for _, l := range lists {
		items := append([]PriceItemContent(nil), l.Items...)
		sort.Slice(items, func(i, j int) bool { return itemKey(items[i]) < itemKey(items[j]) })
		itemDocs := make([]any, 0, len(items))
		for _, it := range items {
			itemDocs = append(itemDocs, map[string]any{
				"definitionCode":     nullableString(it.DefinitionCode),
				"categoryCode":       nullableString(it.CategoryCode),
				"packageCode":        nullableString(it.PackageCode),
				"locationId":         nullableString(it.LocationID),
				"unitType":           it.UnitType,
				"pricingMethod":      it.PricingMethod,
				"amount":             nullableDecimal(it.Amount),
				"percent":            nullableDecimal(it.Percent),
				"formulaKey":         nullableString(it.FormulaKey),
				"minAmount":          nullableDecimal(it.MinAmount),
				"maxAmount":          nullableDecimal(it.MaxAmount),
				"memberShareMethod":  method(it.MemberShareMethod),
				"memberShareAmount":  nullableDecimal(it.MemberShareAmount),
				"memberSharePercent": nullableDecimal(it.MemberSharePercent),
				"validFrom":          DateOnly(it.ValidFrom).Format(time.DateOnly),
				"validTo":            nullableDate(it.ValidTo),
				"priority":           nullableInt(&it.Priority),
			})
		}
		listDocs = append(listDocs, map[string]any{
			"code":        l.Code,
			"name":        l.Name,
			"priority":    nullableInt(&l.Priority),
			"seasonFrom":  nullableDate(l.SeasonFrom),
			"seasonTo":    nullableDate(l.SeasonTo),
			"weekdayMask": nullableInt(l.WeekdayMask),
			"items":       itemDocs,
		})
	}

	packages := append([]PackageContent(nil), c.Packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].Code < packages[j].Code })
	packageDocs := make([]any, 0, len(packages))
	for _, p := range packages {
		lines := append([]PackageLineContent(nil), p.Lines...)
		sort.Slice(lines, func(i, j int) bool { return lines[i].DefinitionCode < lines[j].DefinitionCode })
		lineDocs := make([]any, 0, len(lines))
		for _, l := range lines {
			lineDocs = append(lineDocs, map[string]any{
				"definitionCode":   l.DefinitionCode,
				"includedQuantity": nullableDecimal(l.IncludedQuantity),
			})
		}
		rule := p.InclusionRule
		if rule == "" {
			rule = InclusionAll
		}
		packageDocs = append(packageDocs, map[string]any{
			"code":          p.Code,
			"name":          p.Name,
			"inclusionRule": rule,
			"minLines":      nullableInt(p.MinLines),
			"lines":         lineDocs,
		})
	}

	quotas := append([]QuotaContent(nil), c.Quotas...)
	sort.Slice(quotas, func(i, j int) bool { return quotaKey(quotas[i]) < quotaKey(quotas[j]) })
	quotaDocs := make([]any, 0, len(quotas))
	for _, q := range quotas {
		quotaDocs = append(quotaDocs, map[string]any{
			"locationId":     nullableString(q.LocationID),
			"definitionCode": nullableString(q.DefinitionCode),
			"periodType":     q.PeriodType,
			"periodFrom":     DateOnly(q.PeriodFrom).Format(time.DateOnly),
			"periodTo":       DateOnly(q.PeriodTo).Format(time.DateOnly),
			"capacity":       nullableDecimal(q.Capacity),
			"allowOverdraft": q.AllowOverdraft,
		})
	}

	var termDoc any
	if c.PaymentTerm != nil {
		termDoc = map[string]any{
			"dueDays":          nullableInt(&c.PaymentTerm.DueDays),
			"settlementMethod": c.PaymentTerm.SettlementMethod,
			"taxBehaviour":     c.PaymentTerm.TaxBehaviour,
			"vatRate":          nullableDecimal(c.PaymentTerm.VatRate),
			"lateFeePercent":   nullableDecimal(c.PaymentTerm.LateFeePercent),
		}
	}

	doc := map[string]any{
		"schema":       ConfigurationVersion,
		"validFrom":    nullableDate(c.ValidFrom),
		"validTo":      nullableDate(c.ValidTo),
		"currencyCode": c.CurrencyCode,
		"priceLists":   listDocs,
		"packages":     packageDocs,
		"quotas":       quotaDocs,
		"paymentTerm":  termDoc,
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("contract: canonical configuration: %w", err)
	}
	return out, nil
}

// ConfigurationHash is the SHA-256 of CanonicalConfiguration as lower-case hex, which is
// what contract_version.configuration_hash stores and what the API returns.
func ConfigurationHash(c PriceContent) (string, error) {
	canonical, err := CanonicalConfiguration(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// itemKey orders two price items of one list deterministically; the three targets are
// mutually exclusive, so concatenating them yields a stable key.
func itemKey(i PriceItemContent) string {
	return i.DefinitionCode + "|" + i.CategoryCode + "|" + i.PackageCode + "|" +
		i.LocationID + "|" + DateOnly(i.ValidFrom).Format(time.DateOnly) + "|" + i.PricingMethod
}

func quotaKey(q QuotaContent) string {
	return q.LocationID + "|" + q.DefinitionCode + "|" +
		DateOnly(q.PeriodFrom).Format(time.DateOnly) + "|" + DateOnly(q.PeriodTo).Format(time.DateOnly)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableDecimal(s string) any {
	if s == "" {
		return nil
	}
	return CanonicalDecimal(s)
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	// Numeric values are strings in the canonical form, so no float ever reaches JSON.
	return fmt.Sprintf("%d", *v)
}

func nullableDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return DateOnly(*t).Format(time.DateOnly)
}
