package assistant

import (
	"slices"
	"strings"
	"unicode"

	"budget-tracking/backend/internal/ledger"
)

// entity is a payee, category, or account with every name it goes by,
// each split into words.
type entity struct {
	Named
	names [][]string
}

// words splits text into lowercase words for matching, dropping
// punctuation and the possessive "'s".
func words(text string) []string {
	fields := strings.FieldsFunc(ledger.NormalizeName(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '&'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSuffix(strings.TrimSuffix(field, "'s"), "’s")
		field = strings.Trim(field, "'&")
		if field != "" {
			result = append(result, field)
		}
	}
	return result
}

// stem folds simple English plurals so "groceries" finds "Grocery".
func stem(word string) string {
	switch {
	case len(word) > 4 && strings.HasSuffix(word, "ies"):
		return word[:len(word)-3] + "y"
	case len(word) > 3 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && !strings.HasSuffix(word, "us"):
		return word[:len(word)-1]
	}
	return word
}

func stems(list []string) []string {
	result := make([]string, len(list))
	for i, word := range list {
		result[i] = stem(word)
	}
	return result
}

// stopwords never name a payee, category, or account on their own, even
// when they appear in one, such as "Food and drinks" or "Main account".
var stopwords = func() map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields(`
		a an the and or of in on at to for from by with without about into over under than then that this these those
		is are was were be been being am do does did done doing have has had having will would can could should shall may might must
		i me my mine we our us you your it its they them their he she his her what which who whom whose when where why how
		much many more most less least much few lot lots any some all each every no not none only just also too very per vs versus
		please tell show give list find get got let know want see compare compared comparing between since until till during before after
		spend spent spending spends expense expenses cost costs costing paid pay pays paying payment payments purchase purchases bought buy buying
		income earn earned earning earnings salary received receive receiving made make money cash amount amounts total totals sum
		transaction transactions charge charges bill bills fee fees price
		biggest largest smallest highest lowest top bottom best worst average typical mean median usual usually most often frequent frequently
		payee payees merchant merchants shop shops store stores vendor vendors category categories account accounts card cards bank wallet
		day days daily week weeks weekly month months monthly year years yearly annual annually quarter quarters today yesterday tomorrow
		last past previous next this current recent recently ago so far ytd mtd now ever overall time times once twice
		january february march april may june july august september october november december
		jan feb mar apr jun jul aug sep sept oct nov dec
		monday tuesday wednesday thursday friday saturday sunday weekend weekends weekday weekdays
		eur euro euros usd dollar dollars gbp pound pounds czk koruna crowns crown chf franc francs pln zloty huf forint jpy yen
		up down go went going goes come came coming there here place places thing things stuff
		other else again still yet left saved save saving savings rate net worth balance balances
		like same different change changed changes grow grew increase decreased decrease
		and/or etc ok okay hi hello hey thanks thank
	`) {
		set[word] = true
	}
	return set
}()

// significant reports whether a word may identify an entity by itself.
func significant(word string) bool {
	if stopwords[word] || stopwords[stem(word)] || len([]rune(word)) < 3 {
		return false
	}
	for _, r := range word {
		if !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

type wordSpan struct{ start, end int }

// fullMatches finds entities whose whole name appears in the question,
// longest names first. Each word belongs to one match at most.
func fullMatches(question []string, used []bool, entities []entity) ([]Named, []wordSpan) {
	type candidate struct {
		entity Named
		at     wordSpan
	}
	var candidates []candidate
	stemmed := stems(question)
	for _, item := range entities {
		for _, name := range item.names {
			if len(name) == 0 || len(name) > len(question) {
				continue
			}
			if len(name) == 1 && !significant(name[0]) {
				continue
			}
			target := stems(name)
			for start := 0; start+len(target) <= len(stemmed); start++ {
				if slices.Equal(stemmed[start:start+len(target)], target) {
					candidates = append(candidates, candidate{item.Named, wordSpan{start, start + len(target)}})
				}
			}
		}
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		return (b.at.end - b.at.start) - (a.at.end - a.at.start)
	})
	taken := slices.Clone(used)
	var found []Named
	var spans []wordSpan
	for _, c := range candidates {
		if slices.Contains(taken[c.at.start:c.at.end], true) {
			continue
		}
		for i := c.at.start; i < c.at.end; i++ {
			taken[i] = true
		}
		if !containsNamed(found, c.entity.ID) {
			found = append(found, c.entity)
		}
		spans = append(spans, c.at)
	}
	return found, spans
}

// partialMatches finds entities that share a significant word with the
// question, such as "Lidl" for "Lidl Bratislava".
func partialMatches(question []string, used []bool, entities []entity) ([]Named, []wordSpan) {
	var found []Named
	var spans []wordSpan
	for i, word := range question {
		if used[i] || !significant(word) {
			continue
		}
		target := stem(word)
		matched := false
		for _, item := range entities {
			if containsNamed(found, item.ID) {
				matched = matched || hasWord(item, target)
				continue
			}
			if hasWord(item, target) {
				found = append(found, item.Named)
				matched = true
			}
		}
		if matched {
			spans = append(spans, wordSpan{i, i + 1})
		}
	}
	return found, spans
}

func hasWord(item entity, target string) bool {
	for _, name := range item.names {
		for _, word := range name {
			if stem(word) == target {
				return true
			}
		}
	}
	return false
}

func containsNamed(list []Named, id int64) bool {
	return slices.ContainsFunc(list, func(item Named) bool { return item.ID == id })
}

func mark(used []bool, spans []wordSpan) {
	for _, at := range spans {
		for i := at.start; i < at.end; i++ {
			used[i] = true
		}
	}
}

// leftovers are the significant words no entity or date took.
func leftovers(question []string, used []bool) []string {
	var result []string
	for i, word := range question {
		if !used[i] && significant(word) {
			if _, month := monthNames[word]; !month {
				result = append(result, word)
			}
		}
	}
	return result
}

// loadEntities reads every payee, category, and account with its aliases.
func (r *run) loadEntities() (payees, categories, accounts []entity, err error) {
	if payees, err = r.entities(`SELECT p.id, p.name, p.name FROM payees p
        UNION ALL SELECT p.id, p.name, a.normalized_name FROM payee_aliases a JOIN payees p ON p.id = a.payee_id`); err != nil {
		return
	}
	if categories, err = r.entities(`SELECT c.id, c.name, c.name FROM categories c
        UNION ALL SELECT c.id, c.name, a.normalized_name FROM category_aliases a JOIN categories c ON c.id = a.category_id`); err != nil {
		return
	}
	accounts, err = r.entities(`SELECT id, name, name FROM accounts`)
	return
}

func (r *run) entities(query string) ([]entity, error) {
	rows, err := r.db.QueryContext(r.ctx, query+` ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []entity
	for rows.Next() {
		var id int64
		var name, alias string
		if err := rows.Scan(&id, &name, &alias); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].ID != id {
			result = append(result, entity{Named: Named{ID: id, Name: name}})
		}
		last := &result[len(result)-1]
		last.names = append(last.names, words(alias))
	}
	return result, rows.Err()
}
