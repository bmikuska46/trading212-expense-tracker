package assistant

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Period is an inclusive range of local dates. An empty From starts at the
// first transaction; an empty To has no end. Label names the period, such
// as "September 2026"; Phrase reads in a sentence, such as "in September 2026".
type Period struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Label  string `json:"label"`
	Phrase string `json:"phrase"`
	// Calendar is month, quarter, or year when the period is exactly one,
	// so a comparison moves by whole calendar units.
	Calendar string `json:"calendar,omitempty"`
}

func (p Period) allTime() bool { return p.From == "" && p.To == "" }

// dates writes the period as "1 Sep 2026 to 30 Sep 2026".
func (p Period) dates() string {
	switch {
	case p.allTime():
		return "all time"
	case p.From == "":
		return "until " + dayLabel(p.To)
	case p.To == "":
		return "from " + dayLabel(p.From)
	case p.From == p.To:
		return dayLabel(p.From)
	}
	return dayLabel(p.From) + " to " + dayLabel(p.To)
}

func dateOf(t time.Time) string { return t.Format(time.DateOnly) }

func parseDay(value string) (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, value)
	return t, err == nil
}

func span(from, to time.Time, label, phrase string) Period {
	return Period{From: dateOf(from), To: dateOf(to), Label: label, Phrase: phrase}
}

func monthOf(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC) }

func monthPeriod(start time.Time) Period {
	label := start.Format("January 2006")
	p := span(start, start.AddDate(0, 1, -1), label, "in "+label)
	p.Calendar = "month"
	return p
}

func quarterPeriod(start time.Time) Period {
	label := fmt.Sprintf("Q%d %d", (int(start.Month())-1)/3+1, start.Year())
	p := span(start, start.AddDate(0, 3, -1), label, "in "+label)
	p.Calendar = "quarter"
	return p
}

func yearPeriod(year int) Period {
	label := strconv.Itoa(year)
	p := span(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC), label, "in "+label)
	p.Calendar = "year"
	return p
}

func dayPeriod(day time.Time, phrase string) Period {
	label := dayLabel(dateOf(day))
	if phrase == "" {
		phrase = "on " + label
	}
	return span(day, day, label, phrase)
}

// rolling ends today and starts so the period holds the given span.
func rolling(today time.Time, years, months, days int, name string) Period {
	from := today.AddDate(-years, -months, -days).AddDate(0, 0, 1)
	return span(from, today, "The last "+name, "in the last "+name)
}

var allTime = Period{Label: "All time", Phrase: "overall"}

// resolvePeriod turns Jev's period choice into dates. Named months, years,
// and dates are read from the text. It reports false when the text holds
// no date the choice needs.
func resolvePeriod(key, text string, today time.Time) (Period, bool) {
	month := monthOf(today)
	weekStart := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	quarter := time.Date(today.Year(), time.Month((int(today.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC)
	switch key {
	case "today":
		return dayPeriod(today, "today"), true
	case "yesterday":
		return dayPeriod(today.AddDate(0, 0, -1), "yesterday"), true
	case "this_week":
		return span(weekStart, weekStart.AddDate(0, 0, 6), "This week", "this week"), true
	case "last_week":
		return span(weekStart.AddDate(0, 0, -7), weekStart.AddDate(0, 0, -1), "Last week", "last week"), true
	case "this_month":
		return monthPeriod(month), true
	case "last_month":
		return monthPeriod(month.AddDate(0, -1, 0)), true
	case "this_quarter":
		return quarterPeriod(quarter), true
	case "last_quarter":
		return quarterPeriod(quarter.AddDate(0, -3, 0)), true
	case "this_year":
		return yearPeriod(today.Year()), true
	case "last_year":
		return yearPeriod(today.Year() - 1), true
	case "last_7_days":
		return rolling(today, 0, 0, 7, "7 days"), true
	case "last_30_days":
		return rolling(today, 0, 0, 30, "30 days"), true
	case "last_90_days":
		return rolling(today, 0, 0, 90, "90 days"), true
	case "last_3_months":
		return rolling(today, 0, 3, 0, "3 months"), true
	case "last_6_months":
		return rolling(today, 0, 6, 0, "6 months"), true
	case "last_12_months":
		return rolling(today, 0, 12, 0, "12 months"), true
	case "last_n":
		return lastN(text, today)
	case "all_time":
		return allTime, true
	}
	found := mentions(text, today)
	switch key {
	case "named_month":
		for _, m := range found {
			if m.kind == "month" {
				return monthPeriod(m.from), true
			}
		}
	case "named_year":
		for _, m := range found {
			if m.kind == "year" {
				return yearPeriod(m.from.Year()), true
			}
		}
	case "specific_day":
		for _, m := range found {
			if m.kind == "day" {
				return dayPeriod(m.from, ""), true
			}
		}
	case "since":
		if len(found) > 0 {
			from := found[0].from
			return Period{From: dateOf(from), To: dateOf(today), Label: "Since " + dayLabel(dateOf(from)), Phrase: "since " + dayLabel(dateOf(from))}, true
		}
	case "date_range":
		if len(found) >= 2 {
			from, to := found[0].from, found[len(found)-1].to
			if to.Before(from) {
				from, to = found[len(found)-1].from, found[0].to
			}
			p := span(from, to, dayLabel(dateOf(from))+" to "+dayLabel(dateOf(to)), "between "+dayLabel(dateOf(from))+" and "+dayLabel(dateOf(to)))
			return p, true
		}
	}
	// Jev may name the kind loosely; use whatever date the text holds.
	if len(found) == 1 {
		switch found[0].kind {
		case "month":
			return monthPeriod(found[0].from), true
		case "year":
			return yearPeriod(found[0].from.Year()), true
		case "day":
			return dayPeriod(found[0].from, ""), true
		}
	}
	if len(found) >= 2 {
		return resolvePeriod("date_range", text, today)
	}
	return Period{}, false
}

var numberWords = map[string]int{
	"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "fifteen": 15, "twenty": 20, "thirty": 30, "few": 3, "couple": 2,
}

var lastNPattern = regexp.MustCompile(`(?i)\b(?:last|past|previous|recent)\s+(?:(\d{1,3})|(a|an|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|few|couple)(?:\s+of)?)\s+(day|week|month|year)s?\b`)
var bareNPattern = regexp.MustCompile(`(?i)\b(\d{1,3})\s+(day|week|month|year)s?\b`)

func lastN(text string, today time.Time) (Period, bool) {
	match := lastNPattern.FindStringSubmatch(text)
	var count int
	var unit string
	if match != nil {
		count, unit = numberWords[strings.ToLower(match[2])], strings.ToLower(match[3])
		if match[1] != "" {
			count, _ = strconv.Atoi(match[1])
		}
	} else if bare := bareNPattern.FindStringSubmatch(text); bare != nil {
		count, _ = strconv.Atoi(bare[1])
		unit = strings.ToLower(bare[2])
	}
	if count <= 0 || count > 400 {
		return Period{}, false
	}
	name := strconv.Itoa(count) + " " + unit
	if count != 1 {
		name += "s"
	}
	switch unit {
	case "day":
		return rolling(today, 0, 0, count, name), true
	case "week":
		return rolling(today, 0, 0, 7*count, name), true
	case "month":
		return rolling(today, 0, count, 0, name), true
	case "year":
		return rolling(today, count, 0, 0, name), true
	}
	return Period{}, false
}

// mention is a date, month, or year named in the text.
type mention struct {
	kind     string
	from, to time.Time
	position int
}

var monthNames = map[string]time.Month{
	"january": 1, "jan": 1, "february": 2, "feb": 2, "march": 3, "mar": 3, "april": 4, "apr": 4, "may": 5,
	"june": 6, "jun": 6, "july": 7, "jul": 7, "august": 8, "aug": 8, "september": 9, "sept": 9, "sep": 9,
	"october": 10, "oct": 10, "november": 11, "nov": 11, "december": 12, "dec": 12,
}

const monthPattern = `(january|february|march|april|may|june|july|august|september|october|november|december|sept|jan|feb|mar|apr|jun|jul|aug|sep|oct|nov|dec)\.?`

var datePatterns = []struct {
	kind    string
	pattern *regexp.Regexp
	// read returns year (0 when missing), month, and day (0 for a month).
	read func([]string) (int, int, int)
}{
	{"day", regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b`), func(m []string) (int, int, int) { return atoi(m[1]), atoi(m[2]), atoi(m[3]) }},
	{"day", regexp.MustCompile(`\b(\d{1,2})[./](\d{1,2})[./](\d{4})\b`), func(m []string) (int, int, int) { return atoi(m[3]), atoi(m[2]), atoi(m[1]) }},
	{"day", regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthPattern + `(?:,?\s+(\d{4}))?\b`), func(m []string) (int, int, int) {
		return atoi(m[3]), int(monthNames[strings.ToLower(m[2])]), atoi(m[1])
	}},
	{"day", regexp.MustCompile(`(?i)\b` + monthPattern + `\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?\b`), func(m []string) (int, int, int) {
		return atoi(m[3]), int(monthNames[strings.ToLower(m[1])]), atoi(m[2])
	}},
	{"month", regexp.MustCompile(`\b(\d{4})-(\d{1,2})\b`), func(m []string) (int, int, int) { return atoi(m[1]), atoi(m[2]), 0 }},
	{"month", regexp.MustCompile(`\b(\d{1,2})/(\d{4})\b`), func(m []string) (int, int, int) { return atoi(m[2]), atoi(m[1]), 0 }},
	{"month", regexp.MustCompile(`(?i)\b` + monthPattern + `(?:\s+(\d{4}))?\b`), func(m []string) (int, int, int) {
		return atoi(m[2]), int(monthNames[strings.ToLower(m[1])]), 0
	}},
	{"year", regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`), func(m []string) (int, int, int) { return atoi(m[1]), 0, 0 }},
}

func atoi(value string) int {
	number, _ := strconv.Atoi(value)
	return number
}

// mentions finds the dates, months, and years in the text in reading
// order. A date without a year is the latest one not after today.
func mentions(text string, today time.Time) []mention {
	masked := []byte(text)
	var found []mention
	for _, candidate := range datePatterns {
		for _, bounds := range candidate.pattern.FindAllSubmatchIndex(masked, -1) {
			groups := make([]string, len(bounds)/2)
			for i := range groups {
				if bounds[2*i] >= 0 {
					groups[i] = string(masked[bounds[2*i]:bounds[2*i+1]])
				}
			}
			year, month, day := candidate.read(groups)
			m, ok := build(candidate.kind, year, month, day, today)
			if !ok {
				continue
			}
			m.position = bounds[0]
			found = append(found, m)
			for i := bounds[0]; i < bounds[1]; i++ {
				masked[i] = ' '
			}
		}
	}
	slices.SortFunc(found, func(a, b mention) int { return a.position - b.position })
	return found
}

func build(kind string, year, month, day int, today time.Time) (mention, bool) {
	if kind == "year" {
		start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		return mention{kind: kind, from: start, to: start.AddDate(1, 0, -1)}, true
	}
	if month < 1 || month > 12 {
		return mention{}, false
	}
	guessed := year == 0
	if guessed {
		year = today.Year()
	}
	if kind == "month" {
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		if guessed && start.After(today) {
			start = start.AddDate(-1, 0, 0)
		}
		return mention{kind: kind, from: start, to: start.AddDate(0, 1, -1)}, true
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if day < 1 || date.Month() != time.Month(month) {
		return mention{}, false
	}
	if guessed && date.After(today) {
		date = date.AddDate(-1, 0, 0)
	}
	return mention{kind: kind, from: date, to: date}, true
}

// previousPeriod is the period to compare with: the one just before, in
// whole calendar units when the period is one, or a year earlier.
func previousPeriod(p Period, compare string) (Period, bool) {
	from, okFrom := parseDay(p.From)
	to, okTo := parseDay(p.To)
	if !okFrom || !okTo {
		return Period{}, false
	}
	if compare == "last_year" {
		return relabel(from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0), p.Calendar), true
	}
	switch p.Calendar {
	case "month":
		return relabel(from.AddDate(0, -1, 0), monthOf(from).AddDate(0, 0, -1), "month"), true
	case "quarter":
		return relabel(from.AddDate(0, -3, 0), from.AddDate(0, 0, -1), "quarter"), true
	case "year":
		return relabel(from.AddDate(-1, 0, 0), from.AddDate(0, 0, -1), "year"), true
	}
	days := int(to.Sub(from).Hours()/24) + 1
	return relabel(from.AddDate(0, 0, -days), from.AddDate(0, 0, -1), ""), true
}

// relabel names a range as a calendar unit when it is one.
func relabel(from, to time.Time, calendar string) Period {
	switch {
	case calendar == "month" && from.Day() == 1 && to.Equal(from.AddDate(0, 1, -1)):
		return monthPeriod(from)
	case calendar == "quarter" && from.Day() == 1 && to.Equal(from.AddDate(0, 3, -1)):
		return quarterPeriod(from)
	case calendar == "year" && from.YearDay() == 1 && to.Equal(from.AddDate(1, 0, -1)):
		return yearPeriod(from.Year())
	case from.Equal(to):
		return dayPeriod(from, "")
	}
	label := dayLabel(dateOf(from)) + " to " + dayLabel(dateOf(to))
	return span(from, to, label, "between "+dayLabel(dateOf(from))+" and "+dayLabel(dateOf(to)))
}

// clip ends a period at today, for a fair comparison of a period still
// in progress. It reports whether the end moved.
func clip(p Period, today time.Time) (Period, bool) {
	to, ok := parseDay(p.To)
	if !ok || !to.After(today) {
		return p, false
	}
	from, _ := parseDay(p.From)
	if from.After(today) {
		return p, false
	}
	clipped := p
	clipped.To = dateOf(today)
	clipped.Calendar = ""
	clipped.Label = p.Label + " so far"
	clipped.Phrase = p.Phrase + " so far"
	return clipped, true
}
