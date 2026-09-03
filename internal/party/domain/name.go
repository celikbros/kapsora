package domain

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// turkishLower maps the two letters whose case folding differs from the Unicode default
// in Turkish. They must be replaced before strings.ToLower, which would otherwise turn
// "İ" into "i" plus a combining dot above.
var turkishLower = strings.NewReplacer("İ", "i", "I", "ı")

// Fold prepares a name fragment for storage and search: NFKC normalisation, Turkish
// lower-casing and single spaces. Diacritics are kept, so "Çağla" folds to "çağla" and
// stays distinguishable from "Cagla".
func Fold(s string) string {
	return CollapseSpaces(strings.ToLower(turkishLower.Replace(norm.NFKC.String(s))))
}

// CollapseSpaces trims the ends and reduces every run of whitespace to one space.
func CollapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// CleanName trims and collapses a submitted name without changing its case.
func CleanName(s string) string { return CollapseSpaces(norm.NFKC.String(s)) }

// NormalizedName is the value stored in party.person.normalized_name and matched by the
// list filter: "last, first middle" in folded form (WP-I2-01 section 3.2).
func NormalizedName(first, middle, last string) string {
	given := Fold(first)
	if m := Fold(middle); m != "" {
		given += " " + m
	}
	family := Fold(last)
	switch {
	case family == "" && given == "":
		return ""
	case family == "":
		return given
	case given == "":
		return family
	default:
		return family + ", " + given
	}
}

// DisplayName is the human-readable form returned by the API: "first middle last".
func DisplayName(first, middle, last string) string {
	return CollapseSpaces(CleanName(first) + " " + CleanName(middle) + " " + CleanName(last))
}

// likeEscaper neutralises the LIKE wildcards a user may type; the SQL side uses the
// default backslash escape character.
var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

// SearchPattern folds a list filter into the space-separated tokens the SQL LIKE ALL
// clause expects. An empty result means "no name filter".
func SearchPattern(q string) string { return likeEscaper.Replace(Fold(q)) }
