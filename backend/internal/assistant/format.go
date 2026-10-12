package assistant

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/ledger"
)

// money writes minor units as "€1,234.56" for EUR or "1,234.56 USD".
func money(minor int64, currency string) string {
	digits, err := ledger.MinorUnits(currency)
	if err != nil {
		digits = 2
	}
	negative := minor < 0
	if negative {
		minor = -minor
	}
	whole, fraction := minor, int64(0)
	if digits > 0 {
		scale := int64(math.Pow10(digits))
		whole, fraction = minor/scale, minor%scale
	}
	text := grouped(whole)
	if digits > 0 {
		text += "." + fmt.Sprintf("%0*d", digits, fraction)
	}
	if currency == "EUR" {
		text = "€" + text
	} else {
		text += " " + currency
	}
	if negative {
		text = "-" + text
	}
	return text
}

func euros(cents int64) string { return money(cents, "EUR") }

func grouped(number int64) string {
	digits := strconv.FormatInt(number, 10)
	var builder strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	return builder.String()
}

// percent writes a fraction as "12.5%".
func percent(fraction float64) string {
	value := fraction * 100
	if math.Abs(value) >= 10 {
		return strconv.FormatFloat(value, 'f', 0, 64) + "%"
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + "%"
}

func count(number int64, singular, plural string) string {
	if number == 1 {
		return "1 " + singular
	}
	return grouped(number) + " " + plural
}

func dayLabel(date string) string {
	day, ok := parseDay(date)
	if !ok {
		return date
	}
	return day.Format("2 Jan 2006")
}

// joinNames writes "A", "A and B", or "A, B, and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}

func namesOf(items []Named) []string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.Name
	}
	return names
}

func upperFirst(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

func daysBetween(from, to time.Time) int { return int(to.Sub(from).Hours()/24) + 1 }

// ratio divides, reporting false for a zero denominator.
func ratio(part, whole int64) (float64, bool) {
	if whole == 0 {
		return 0, false
	}
	return float64(part) / float64(whole), true
}

// change describes a difference as "€12.00 more (up 5%)".
func change(current, previous int64, more, less string) string {
	difference := current - previous
	if difference == 0 {
		return "exactly the same"
	}
	word := more
	if difference < 0 {
		word, difference = less, -difference
	}
	text := euros(difference) + " " + word
	if fraction, ok := ratio(current-previous, abs(previous)); ok {
		direction := "up"
		if fraction < 0 {
			direction = "down"
		}
		text += " (" + direction + " " + percent(math.Abs(fraction)) + ")"
	}
	return text
}

func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
