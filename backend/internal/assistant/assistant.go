// Package assistant answers questions about the ledger in a chat. TypeSafe
// Jev reads a question into typed choices: what is asked, the period, the
// direction of money, and whether it names a payee, category, or account.
// Code finds those names in the question and answers with fixed queries, so
// no generated SQL ever runs and only the question text leaves the server.
package assistant

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"budget-tracking/backend/internal/integrations"
)

// MaxQuestionLength is the longest question in runes.
const MaxQuestionLength = 500

// Ask evaluates typed questions about state with TypeSafe Jev.
type Ask func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error)

// Turn is an earlier question in the chat and how it was read, so a
// follow-up such as "and last month?" can reuse it.
type Turn struct {
	Question       string
	Interpretation Interpretation
}

// Interpretation is how a question was read, after code resolved the
// period and the names. It is stored with the answer.
type Interpretation struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Period     Period  `json:"period"`
	// PeriodGiven is false when the question named no period and the
	// intent's default was used.
	PeriodGiven bool       `json:"periodGiven"`
	Direction   string     `json:"direction"`
	Order       string     `json:"order"`
	Step        string     `json:"step"`
	Compare     string     `json:"compare"`
	Payees      []Named    `json:"payees,omitempty"`
	Categories  []Named    `json:"categories,omitempty"`
	Accounts    []Named    `json:"accounts,omitempty"`
	Limit       int        `json:"limit,omitempty"`
	MinCents    int64      `json:"minCents,omitempty"`
	MaxCents    int64      `json:"maxCents,omitempty"`
	Search      string     `json:"search,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	Unmatched   []string   `json:"unmatched,omitempty"`
	Options     []Fallback `json:"options,omitempty"`
}

// Named is a payee, category, or account the question names.
type Named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Fallback is another reading Jev found likely, offered when it was unsure.
type Fallback struct {
	Intent      string  `json:"intent"`
	Probability float64 `json:"probability"`
}

// Answer is what the chat shows. Money cells in tables are minor units of
// the column currency, EUR cents unless the column names another.
type Answer struct {
	Text        string   `json:"text"`
	Table       *Table   `json:"table,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	Link        *Link    `json:"link,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

type Table struct {
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Column kinds are text, money, count, percent (a fraction), and date.
// Bar draws the money or count as a bar against the largest value.
type Column struct {
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Currency string `json:"currency,omitempty"`
	Bar      bool   `json:"bar,omitempty"`
}

// CleanQuestion trims a question and reports whether it can be asked.
func CleanQuestion(question string) (string, bool) {
	question = strings.TrimSpace(question)
	return question, question != "" && utf8.ValidString(question) && utf8.RuneCountInString(question) <= MaxQuestionLength
}

// Respond reads the question with Jev and answers it from the ledger. now
// is the current time in the owner's timezone. Only the question and the
// previous question of the chat are sent to TypeSafe.
func Respond(ctx context.Context, db *sql.DB, ask Ask, question string, previous *Turn, now time.Time) (Answer, Interpretation, error) {
	var prior *string
	if previous != nil {
		prior = &previous.Question
	}
	reading, err := interpret(ctx, ask, question, prior)
	if err != nil {
		return Answer{}, Interpretation{}, err
	}
	r := &run{ctx: ctx, db: db, today: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)}
	in, err := r.resolve(reading, question, previous)
	if err != nil {
		return Answer{}, Interpretation{}, err
	}
	answer, err := r.answer(in)
	if err != nil {
		return Answer{}, Interpretation{}, err
	}
	return answer, in, nil
}

// Examples are questions shown in a new chat, grouped by what they cover.
func Examples() []ExampleGroup { return exampleGroups }

type ExampleGroup struct {
	Title     string   `json:"title"`
	Questions []string `json:"questions"`
}

// Intents lists the keys of every kind of question the assistant answers.
func Intents() []string {
	keys := make([]string, len(intents))
	for index, item := range intents {
		keys[index] = item.key
	}
	return keys
}
