package ledger

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxNameLength = 100

var folder = cases.Fold()

// NormalizeName returns the comparison key for payee and category names.
// Equivalent spellings such as "RETAIL_STORES", "Retail  stores", and the
// full-width form of the same text share one key. Punctuation is kept, so
// "A&B" and "AB" remain different names.
func NormalizeName(name string) string {
	folded := folder.String(norm.NFKC.String(name))
	return strings.Join(strings.FieldsFunc(folded, func(r rune) bool {
		return unicode.IsSpace(r) || r == '_' || unicode.IsControl(r)
	}), " ")
}

// DisplayName tidies whitespace and control characters without changing case.
func DisplayName(name string) string {
	cleaned := strings.Join(strings.FieldsFunc(norm.NFC.String(name), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	return truncateRunes(cleaned, MaxNameLength)
}

// LabelName turns a source category label such as "RETAIL_STORES" into
// "Retail stores". Labels in mixed case keep their spelling.
func LabelName(label string) string {
	name := DisplayName(strings.ReplaceAll(label, "_", " "))
	if name == "" || strings.ToUpper(name) != name || strings.ToLower(name) == name {
		return name
	}
	lower := []rune(strings.ToLower(name))
	lower[0] = unicode.ToUpper(lower[0])
	return string(lower)
}

// ValidName reports whether a display name can be stored.
func ValidName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= MaxNameLength && NormalizeName(name) != ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return strings.TrimSpace(string([]rune(value)[:limit]))
}
