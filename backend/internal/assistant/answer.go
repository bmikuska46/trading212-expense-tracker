package assistant

import (
	"fmt"
	"strings"
)

// answer runs the fixed queries for the interpreted question.
func (r *run) answer(in Interpretation) (Answer, error) {
	if len(in.Unmatched) > 0 {
		return unmatched(in), nil
	}
	if in.Confidence < lowConfidence && len(in.Options) > 0 && !conversational(in.Intent) {
		return clarify(in), nil
	}
	if (in.Intent == "spending_total" || in.Intent == "income_total") && in.Step != "not_mentioned" {
		in.Intent = "average"
	}
	var result Answer
	var err error
	switch in.Intent {
	case "spending_total", "income_total":
		result, err = r.total(in)
	case "net_flow":
		result, err = r.netFlow(in)
	case "savings_rate":
		result, err = r.savingsRate(in)
	case "summary":
		result, err = r.summary(in)
	case "average":
		result, err = r.average(in)
	case "projection":
		result, err = r.projection(in)
	case "compare_periods":
		result, err = r.comparePeriods(in)
	case "category_changes":
		result, err = r.categoryChanges(in)
	case "top_payees":
		result, err = r.topPayees(in)
	case "top_categories":
		result, err = r.topCategories(in)
	case "top_accounts":
		result, err = r.topAccounts(in)
	case "frequent_payees":
		result, err = r.frequentPayees(in)
	case "largest_transactions":
		result, err = r.largestTransactions(in)
	case "by_month":
		result, err = r.byPeriod(in, "month")
	case "by_year":
		result, err = r.byPeriod(in, "year")
	case "by_weekday":
		result, err = r.byWeekday(in)
	case "by_day":
		result, err = r.byDay(in)
	case "transaction_count":
		result, err = r.transactionCount(in)
	case "last_transaction":
		result, err = r.edgeTransaction(in, true)
	case "first_transaction":
		result, err = r.edgeTransaction(in, false)
	case "list_transactions":
		result, err = r.listTransactions(in)
	case "recurring":
		result, err = r.recurring(in)
	case "new_payees":
		result, err = r.newPayees(in)
	case "uncategorized":
		result, err = r.uncategorized(in)
	case "refunds":
		result, err = r.refunds(in)
	case "transfers":
		result, err = r.transfers(in)
	case "account_balances":
		result, err = r.accountBalances(in)
	case "net_worth":
		result, err = r.netWorth(in)
	case "portfolio_value":
		result, err = r.portfolioValue(in)
	case "portfolio_change":
		result, err = r.portfolioChange(in)
	case "pie_performance":
		result, err = r.piePerformance(in)
	case "dividends":
		result, err = r.dividends(in)
	case "review_status":
		result, err = r.reviewStatus()
	case "imports_status":
		result, err = r.importsStatus(in)
	case "list_categories":
		result, err = r.listCategories(in)
	case "data_overview":
		result, err = r.dataOverview()
	case "exchange_rate":
		result, err = r.exchangeRate(in)
	case "help":
		result = help()
	case "greeting":
		result = greeting()
	default:
		result = unsupported(in)
	}
	if err != nil {
		return Answer{}, err
	}
	if result.Suggestions == nil {
		result.Suggestions = intentByKey[in.Intent].follow
	}
	if example := intentByKey[in.Intent].example; in.Confidence < lowConfidence && !conversational(in.Intent) && in.Intent != "unsupported" && example != "" {
		result.Notes = append(result.Notes, fmt.Sprintf("I was not sure what you meant, so I answered it like \"%s\". Rephrase the question if you meant something else.", example))
	}
	return result, nil
}

func conversational(key string) bool {
	return key == "help" || key == "greeting"
}

// phrase is the period as it reads at the end of a sentence.
func phrase(p Period) string {
	if p.Phrase == "" {
		return ""
	}
	return " " + p.Phrase
}

func examplesOf(keys ...string) []string {
	var result []string
	for _, key := range keys {
		if item, ok := intentByKey[key]; ok && item.example != "" && !conversational(key) && key != "unsupported" && key != "follow_up" {
			result = append(result, item.example)
		}
	}
	return result
}

func clarify(in Interpretation) Answer {
	keys := []string{in.Intent}
	for _, option := range in.Options {
		keys = append(keys, option.Intent)
	}
	suggestions := examplesOf(keys...)
	if len(suggestions) == 0 {
		suggestions = exampleGroups[0].Questions
	}
	return Answer{
		Text:        "I'm not sure what you're asking. Did you mean one of these?",
		Suggestions: suggestions,
	}
}

func unmatched(in Interpretation) Answer {
	kind, rest := in.Unmatched[0], in.Unmatched[1:]
	plural := map[string]string{"payee": "payees", "category": "categories", "account": "accounts"}[kind]
	text := fmt.Sprintf("I couldn't find a %s matching \"%s\" in your data.", kind, strings.Join(rest, " "))
	return Answer{
		Text:  text,
		Notes: []string{"Names are matched against your " + plural + " and their aliases, and transaction notes are searched too. Try the exact name, or put text in quotes to search for it."},
		Link:  &Link{Label: "Open " + upperFirst(plural), Href: "/" + plural + "/"},
		Suggestions: map[string][]string{
			"payee":    {"Who is my biggest payee?", "Where do I shop most often?"},
			"category": {"What categories do I have?", "What do I spend the most on?"},
			"account":  {"What are my account balances?"},
		}[kind],
	}
}

func help() Answer {
	table := &Table{Columns: []Column{{Label: "Topic", Kind: "text"}, {Label: "Example question", Kind: "text"}}}
	for _, group := range exampleGroups {
		for _, question := range group.Questions {
			table.Rows = append(table.Rows, []any{group.Title, question})
		}
	}
	return Answer{
		Text: "I answer questions about the money in this app: spending and income over any period, payees, categories, and accounts, " +
			"comparisons and trends, averages and projections, subscriptions, balances, net worth, and your investments. " +
			"Name a period (\"last month\", \"in March\", \"since 2024\"), a payee, category, or account, an amount (\"over 100 EUR\"), " +
			"or text in quotes to narrow any question. Follow-ups such as \"and last year?\" reuse the previous question.",
		Table:       table,
		Notes:       []string{"Only your question and the previous question in this chat are sent to TypeSafe. Your ledger never leaves this server."},
		Suggestions: []string{"How much did I spend this month?", "Who is my biggest payee?", "What is my net worth?"},
	}
}

func greeting() Answer {
	return Answer{
		Text:        "Hi! Ask me anything about your spending, income, accounts, or investments.",
		Suggestions: []string{"How much did I spend this month?", "Give me a summary of last month", "What can you answer?"},
	}
}

func unsupported(in Interpretation) Answer {
	keys := []string{}
	for _, option := range in.Options {
		keys = append(keys, option.Intent)
	}
	suggestions := examplesOf(keys...)
	if len(suggestions) == 0 {
		suggestions = []string{"What can you answer?", "How much did I spend this month?", "Who is my biggest payee?"}
	}
	return Answer{
		Text: "I can only answer questions about the data in this app, such as spending, income, payees, categories, accounts, and investments. " +
			"I can't give financial advice, set budgets, or change your data.",
		Suggestions: suggestions,
	}
}
