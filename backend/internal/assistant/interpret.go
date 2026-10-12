package assistant

import (
	"context"
	"slices"

	"budget-tracking/backend/internal/integrations"
)

// Choices for the period, direction, order, step, and comparison. Code
// reads specific months, years, dates, and counts from the question text.
var (
	periodChoices = map[string]any{
		"not_mentioned":  "The question names no time period",
		"today":          "Today",
		"yesterday":      "Yesterday",
		"this_week":      "This week",
		"last_week":      "Last week, the previous calendar week",
		"this_month":     "This month, the current calendar month",
		"last_month":     "Last month, the previous calendar month",
		"this_quarter":   "This quarter",
		"last_quarter":   "Last quarter, the previous calendar quarter",
		"this_year":      "This year, the current calendar year, year to date",
		"last_year":      "Last year, the previous calendar year",
		"last_7_days":    "The last 7 days or the past week counted in days",
		"last_30_days":   "The last 30 days",
		"last_90_days":   "The last 90 days",
		"last_3_months":  "The last 3 months or the past quarter counted back from today",
		"last_6_months":  "The last 6 months or half a year",
		"last_12_months": "The last 12 months or the past year counted back from today",
		"last_n":         "Another number of recent days, weeks, months, or years, such as the last 2 weeks or the past 2 years",
		"all_time":       "All time, ever, in total, since the beginning",
		"named_month":    "One named month, such as March or March 2025",
		"named_year":     "One named year, such as 2024",
		"specific_day":   "One specific date, such as 3 March or 2025-03-03",
		"date_range":     "Between two named dates or months, such as from January to March",
		"since":          "Since a named date, month, or year until today",
	}
	directionChoices = map[string]any{
		"not_mentioned": "The question does not say whether money went out or came in",
		spending:        "Money going out: spending, expenses, purchases, payments, costs",
		income:          "Money coming in: income, salary, earnings, money received",
		both:            "Both money in and money out together",
	}
	orderChoices = map[string]any{
		"not_mentioned": "The question asks for no ranking",
		"most":          "The most, biggest, largest, highest, top, or most expensive",
		"least":         "The least, smallest, lowest, cheapest, or bottom",
	}
	stepChoices = map[string]any{
		"not_mentioned": "The question names no unit for an average or breakdown",
		"day":           "Per day or daily",
		"week":          "Per week or weekly",
		"month":         "Per month or monthly",
		"year":          "Per year or yearly",
		"transaction":   "Per transaction, purchase, payment, or visit",
	}
	compareChoices = map[string]any{
		"not_mentioned": "The question compares with no other period, or with the one just before",
		"previous":      "The period just before, such as last month compared with the month before",
		"last_year":     "The same period a year earlier",
	}
)

// reading is Jev's answer before code resolves names and dates.
type reading struct {
	intent     string
	confidence float64
	options    []Fallback
	period     string
	direction  string
	order      string
	step       string
	compare    string
	// Probabilities that the question names a payee, category, or account.
	payee, category, account float64
}

const (
	questionText   = "`question` is a message the owner wrote in the chat of their personal budgeting app."
	previousText   = " `previous question`, when present, is the message they sent before it in the same chat."
	minimumOptions = 0.15
)

func choiceOf(question string, criteria map[string]any) integrations.Question {
	return integrations.Question{Type: integrations.QuestionChoice, Instructions: map[string]any{"question": question}, Criteria: criteria}
}

func noulOf(question string) integrations.Question {
	return integrations.Question{Type: integrations.QuestionNoul, Instructions: map[string]any{"question": question}}
}

// interpret sends the question, and the previous question for follow-ups,
// in one request. Nothing from the ledger is sent.
func interpret(ctx context.Context, ask Ask, question string, previous *string) (reading, error) {
	state := map[string]any{"question": question}
	if previous != nil {
		state["previous question"] = *previous
	}
	intentCriteria := make(map[string]any, len(intents))
	for _, item := range intents {
		if item.key == "follow_up" && previous == nil {
			continue
		}
		intentCriteria[item.key] = item.description
	}
	questions := map[string]integrations.Question{
		"intent":    choiceOf(questionText+previousText+" What does the owner want to know?", intentCriteria),
		"period":    choiceOf(questionText+" Which time period does `question` ask about?", periodChoices),
		"direction": choiceOf(questionText+" Does `question` ask about money going out, money coming in, or both?", directionChoices),
		"order":     choiceOf(questionText+" Does `question` ask for the most or the least of something?", orderChoices),
		"step":      choiceOf(questionText+" Does `question` ask for an amount per day, week, month, year, or transaction?", stepChoices),
		"compare":   choiceOf(questionText+" Does `question` compare with an earlier period, and which one?", compareChoices),
		"payee":     noulOf(questionText + " Does `question` name a specific merchant, shop, company, service, or person that money was paid to or received from?"),
		"category":  noulOf(questionText + " Does `question` name a specific spending or income category, such as groceries, rent, travel, or salary?"),
		"account":   noulOf(questionText + " Does `question` name a specific bank account, card, wallet, or broker account of the owner?"),
	}
	answers, err := ask(ctx, state, questions)
	if err != nil {
		return reading{}, err
	}
	intentAnswer := answers["intent"]
	result := reading{
		intent:     intentAnswer.Choice,
		confidence: intentAnswer.Confidence,
		period:     known(answers["period"].Choice, periodChoices),
		direction:  known(answers["direction"].Choice, directionChoices),
		order:      known(answers["order"].Choice, orderChoices),
		step:       known(answers["step"].Choice, stepChoices),
		compare:    known(answers["compare"].Choice, compareChoices),
		payee:      answers["payee"].Noul,
		category:   answers["category"].Noul,
		account:    answers["account"].Noul,
	}
	if _, ok := intentByKey[result.intent]; !ok || (result.intent == "follow_up" && previous == nil) {
		result.intent, result.confidence = "unsupported", 0
	}
	for key, probability := range intentAnswer.Probabilities {
		if _, ok := intentByKey[key]; ok && key != result.intent && probability >= minimumOptions {
			result.options = append(result.options, Fallback{Intent: key, Probability: probability})
		}
	}
	slices.SortFunc(result.options, func(a, b Fallback) int {
		if a.Probability != b.Probability {
			if a.Probability > b.Probability {
				return -1
			}
			return 1
		}
		if a.Intent < b.Intent {
			return -1
		}
		return 1
	})
	if len(result.options) > 3 {
		result.options = result.options[:3]
	}
	return result, nil
}

// known keeps an answer that is one of the offered options.
func known(choice string, criteria map[string]any) string {
	if _, ok := criteria[choice]; ok {
		return choice
	}
	return "not_mentioned"
}
