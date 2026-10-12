package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"budget-tracking/backend/internal/assistant"
	"budget-tracking/backend/internal/integrations"
)

// chatReading is what the fake Jev answers for one question. Empty fields
// answer "not_mentioned".
type chatReading struct {
	intent, period, direction, order, step, compare string
	confidence                                      float64
	payee, category, account                        float64
}

// chatJev reads questions from a table the test fills in.
type chatJev struct {
	mu       sync.Mutex
	bodies   [][]byte
	readings map[string]chatReading
	failing  bool
}

func newChatJev(t *testing.T) (*chatJev, *integrations.Client) {
	fake := &chatJev{readings: map[string]chatReading{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/typesafe/models":
			w.Write([]byte(`{"models":[{"name":"` + integrations.JevModel + `"}]}`))
		case "/typesafe/systemone":
			body, _ := io.ReadAll(r.Body)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.bodies = append(fake.bodies, body)
			if fake.failing {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var request struct {
				State     map[string]string `json:"state"`
				Questions map[string]struct {
					Type     string         `json:"type"`
					Criteria map[string]any `json:"criteria"`
				} `json:"questions"`
			}
			if json.Unmarshal(body, &request) != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			reading, ok := fake.readings[request.State["question"]]
			if !ok {
				reading = chatReading{intent: "unsupported"}
			}
			confidence := reading.confidence
			if confidence == 0 {
				confidence = 0.9
			}
			choices := map[string]string{"intent": reading.intent, "period": reading.period, "direction": reading.direction,
				"order": reading.order, "step": reading.step, "compare": reading.compare}
			nouls := map[string]float64{"payee": reading.payee, "category": reading.category, "account": reading.account}
			answers := map[string]integrations.Answer{}
			for id, question := range request.Questions {
				if question.Type == integrations.QuestionNoul {
					answers[id] = integrations.Answer{Type: question.Type, Noul: nouls[id]}
					continue
				}
				choice := choices[id]
				if choice == "" {
					choice = "not_mentioned"
				}
				if _, offered := question.Criteria[choice]; !offered {
					w.WriteHeader(http.StatusUnprocessableEntity)
					fmt.Fprintf(w, "%s is not offered for %s", choice, id)
					return
				}
				answers[id] = integrations.Answer{Type: question.Type, Choice: choice,
					Probabilities: map[string]float64{choice: confidence}, Confidence: confidence}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "jev-1", "answers": answers})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := integrations.NewClient()
	client.TypeSafe = server.URL + "/typesafe"
	return fake, client
}

type chatAnswer struct {
	Chat    chatView `json:"chat"`
	Message struct {
		ID       int64            `json:"id"`
		Question string           `json:"question"`
		Answer   assistant.Answer `json:"answer"`
	} `json:"message"`
}

type chatList struct {
	Chats    []chatView               `json:"chats"`
	Examples []assistant.ExampleGroup `json:"examples"`
}

type chatHistory struct {
	chatView
	Messages []struct {
		Question string           `json:"question"`
		Answer   assistant.Answer `json:"answer"`
	} `json:"messages"`
}

// chatLedger seeds three months of a small ledger.
func chatLedger(app *testApp, cookie *http.Cookie) {
	app.t.Helper()
	checking, savings := app.account(cookie, "Checking"), app.account(cookie, "Savings")
	groceries, rent, salary, streaming := app.category(cookie, "Groceries"), app.category(cookie, "Rent"), app.category(cookie, "Salary"), app.category(cookie, "Subscriptions")
	add := func(date, kind, amount, payee string, category int64, notes string) transactionView {
		entry := map[string]any{"accountId": checking, "date": date, "kind": kind, "amount": amount, "payee": payee, "notes": notes}
		if category != 0 {
			entry["categoryId"] = category
		}
		return app.transaction(cookie, entry)
	}
	for _, month := range []string{"2026-07", "2026-08", "2026-09"} {
		add(month+"-01", "expense", "900", "Example Landlord", rent, "")
		add(month+"-03", "expense", "12.99", "Sample Streaming", streaming, "")
	}
	add("2026-06-03", "expense", "12.99", "Sample Streaming", streaming, "")
	add("2026-07-25", "income", "3000", "Example Employer", salary, "")
	add("2026-08-25", "income", "3000", "Example Employer", salary, "")
	add("2026-08-10", "expense", "40", "Example Market", groceries, "weekly shop")
	add("2026-08-17", "expense", "55", "Example Market", groceries, "weekly shop")
	market := add("2026-09-12", "expense", "62", "Example Market", groceries, "weekly shop")
	add("2026-09-14", "expense", "25", "Sample Kiosk", 0, "birthday card")
	app.transaction(cookie, map[string]any{"accountId": checking, "date": "2026-09-15", "kind": "refund", "amount": "10", "refundOf": market.ID})
	app.transaction(cookie, map[string]any{"accountId": checking, "toAccountId": savings, "date": "2026-09-16", "kind": "transfer", "amount": "500"})
}

func (app *testApp) ask(cookie *http.Cookie, chat int64, question string) chatAnswer {
	app.t.Helper()
	if chat == 0 {
		return decode[chatAnswer](app.t, app.expect(http.StatusCreated, "POST", "/api/chats", map[string]string{"question": question}, cookie))
	}
	return decode[chatAnswer](app.t, app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/chats/%d/messages", chat), map[string]string{"question": question}, cookie))
}

func TestChatHistoryAndPrivacy(t *testing.T) {
	fake, client := newChatJev(t)
	app := newTestApp(t, Options{Integrations: client, Now: func() time.Time { return localTime("2026-09-20", 12, 0) }})
	cookie := app.setup()
	question := map[string]string{"question": "How much did I spend this month?"}

	app.expect(http.StatusConflict, "POST", "/api/chats", question, cookie)
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": false}, cookie)
	for _, invalid := range []string{"", "   ", strings.Repeat("a", assistant.MaxQuestionLength+1)} {
		fields := decode[errorBody](t, app.expect(http.StatusUnprocessableEntity, "POST", "/api/chats", map[string]string{"question": invalid}, cookie)).Fields
		if fields["question"] == "" {
			t.Fatalf("question error for %q: %v", invalid, fields)
		}
	}

	chatLedger(app, cookie)
	fake.readings["How much did I spend this month?"] = chatReading{intent: "spending_total", period: "this_month", direction: "spending"}
	fake.readings["and at Example Market?"] = chatReading{intent: "follow_up", payee: 0.9}
	fake.readings["And in August"] = chatReading{intent: "follow_up", period: "named_month"}

	first := app.ask(cookie, 0, "How much did I spend this month?")
	// September: rent 900, streaming 12.99, market 62 - 10 refunded, kiosk 25.
	if !strings.Contains(first.Message.Answer.Text, "€989.99") || first.Chat.Title != "How much did I spend this month?" {
		t.Fatalf("first answer: %+v", first)
	}
	follow := app.ask(cookie, first.Chat.ID, "and at Example Market?")
	if !strings.Contains(follow.Message.Answer.Text, "€52.00") || !strings.Contains(follow.Message.Answer.Text, "Example Market") {
		t.Fatalf("a follow-up keeps the period and adds the payee: %+v", follow.Message.Answer)
	}
	august := app.ask(cookie, first.Chat.ID, "And in August")
	if !strings.Contains(august.Message.Answer.Text, "€95.00") {
		t.Fatalf("a follow-up changes the period and keeps the payee: %+v", august.Message.Answer)
	}

	fake.mu.Lock()
	bodies := fake.bodies
	fake.mu.Unlock()
	for _, body := range bodies {
		for _, private := range []string{"Landlord", "Employer", "Checking", "Groceries", "900", "3000"} {
			if bytes.Contains(body, []byte(private)) {
				t.Fatalf("ledger data must never reach TypeSafe, found %q: %s", private, body)
			}
		}
	}
	if last := bodies[len(bodies)-1]; !bytes.Contains(last, []byte(`"previous question":"and at Example Market?"`)) {
		t.Fatalf("a follow-up sends the previous question: %s", last)
	}

	second := app.ask(cookie, 0, "How much did I spend this month?")
	list := decode[chatList](t, app.expect(http.StatusOK, "GET", "/api/chats", nil, cookie))
	if len(list.Chats) != 2 || list.Chats[0].ID != second.Chat.ID || len(list.Examples) == 0 {
		t.Fatalf("chats newest first with examples: %+v", list)
	}
	history := decode[chatHistory](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/chats/%d", first.Chat.ID), nil, cookie))
	if len(history.Messages) != 3 || history.Messages[1].Question != "and at Example Market?" || history.Messages[2].Answer.Text != august.Message.Answer.Text {
		t.Fatalf("history: %+v", history)
	}

	renamed := decode[chatView](t, app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/chats/%d", first.Chat.ID), map[string]string{"title": "Market"}, cookie))
	if renamed.Title != "Market" {
		t.Fatalf("rename: %+v", renamed)
	}
	app.expect(http.StatusUnprocessableEntity, "PATCH", fmt.Sprintf("/api/chats/%d", first.Chat.ID), map[string]string{"title": " "}, cookie)

	fake.mu.Lock()
	fake.failing = true
	fake.mu.Unlock()
	app.expect(http.StatusBadGateway, "POST", fmt.Sprintf("/api/chats/%d/messages", first.Chat.ID), question, cookie)
	if history := decode[chatHistory](t, app.expect(http.StatusOK, "GET", fmt.Sprintf("/api/chats/%d", first.Chat.ID), nil, cookie)); len(history.Messages) != 3 {
		t.Fatalf("a failed answer stores nothing: %d", len(history.Messages))
	}

	app.expect(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/chats/%d", first.Chat.ID), nil, cookie)
	app.expect(http.StatusNotFound, "GET", fmt.Sprintf("/api/chats/%d", first.Chat.ID), nil, cookie)
	app.expect(http.StatusNotFound, "POST", fmt.Sprintf("/api/chats/%d/messages", first.Chat.ID), question, cookie)
	app.expect(http.StatusNotFound, "DELETE", fmt.Sprintf("/api/chats/%d", first.Chat.ID), nil, cookie)
	app.expect(http.StatusUnauthorized, "GET", "/api/chats", nil, nil)
}

func TestChatAnswersEveryIntent(t *testing.T) {
	fake, client := newChatJev(t)
	app := newTestApp(t, Options{Integrations: client, Now: func() time.Time { return localTime("2026-09-20", 12, 0) }})
	cookie := app.setup()
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)
	chatLedger(app, cookie)

	cases := []struct {
		question string
		reading  chatReading
		want     []string
	}{
		{"How much did I spend this month?", chatReading{intent: "spending_total", period: "this_month"}, []string{"€989.99"}},
		{"How much did I spend on Groceries in August 2026?", chatReading{intent: "spending_total", period: "named_month", category: 0.9}, []string{"€95.00"}},
		{"How much did I spend over 50 EUR?", chatReading{intent: "spending_total", period: "all_time"}, []string{"€2,817.00"}},
		{"How much income did I get this year?", chatReading{intent: "income_total", period: "this_year"}, []string{"€6,000.00"}},
		{"How much did I save last month?", chatReading{intent: "net_flow", period: "last_month"}, []string{"€1,992.01"}},
		{"What is my savings rate this year?", chatReading{intent: "savings_rate", period: "this_year"}, nil},
		{"Give me a summary of last month", chatReading{intent: "summary", period: "last_month"}, []string{"Rent"}},
		{"What is my average monthly spending?", chatReading{intent: "average", step: "month"}, nil},
		{"What is my average purchase at Example Market?", chatReading{intent: "average", step: "transaction", payee: 0.9}, nil},
		{"How much do I spend per week?", chatReading{intent: "spending_total", step: "week"}, nil},
		{"How much will I spend this month at this pace?", chatReading{intent: "projection", period: "this_month"}, nil},
		{"Did I spend more this month than last month?", chatReading{intent: "compare_periods", period: "this_month", compare: "previous"}, nil},
		{"Did I spend more this month than a year ago?", chatReading{intent: "compare_periods", period: "this_month", compare: "last_year"}, nil},
		{"Which categories grew the most compared with last month?", chatReading{intent: "category_changes", period: "this_month", compare: "previous"}, nil},
		{"Who is my biggest payee?", chatReading{intent: "top_payees", order: "most"}, []string{"Example Landlord", "€2,700.00"}},
		{"Who pays me the most?", chatReading{intent: "top_payees", order: "most", direction: "income"}, []string{"Example Employer"}},
		{"Who is my smallest payee?", chatReading{intent: "top_payees", order: "least"}, nil},
		{"What do I spend the most on?", chatReading{intent: "top_categories", order: "most"}, []string{"Rent"}},
		{"Which account do I spend the most from?", chatReading{intent: "top_accounts", order: "most"}, []string{"Checking"}},
		{"Where do I shop most often?", chatReading{intent: "frequent_payees"}, nil},
		{"What were my 3 biggest purchases this year?", chatReading{intent: "largest_transactions", period: "this_year", order: "most"}, nil},
		{"What was my cheapest purchase?", chatReading{intent: "largest_transactions", order: "least"}, nil},
		{"Show my spending by month", chatReading{intent: "by_month", direction: "spending"}, nil},
		{"Show income and spending by month", chatReading{intent: "by_month", direction: "both"}, nil},
		{"How much did I spend each year?", chatReading{intent: "by_year", direction: "spending"}, nil},
		{"Which day of the week do I spend the most?", chatReading{intent: "by_weekday"}, nil},
		{"On which days did I spend the most this month?", chatReading{intent: "by_day", period: "this_month"}, nil},
		{"How many times did I pay at Example Market?", chatReading{intent: "transaction_count", direction: "spending", payee: 0.9}, []string{"3 purchases"}},
		{"When did I last get paid?", chatReading{intent: "last_transaction", direction: "income"}, []string{"25 Aug 2026"}},
		{"When was my first transaction?", chatReading{intent: "first_transaction"}, nil},
		{"Show my transactions over 50 EUR this month", chatReading{intent: "list_transactions", period: "this_month"}, nil},
		{`Show transactions with "birthday"`, chatReading{intent: "list_transactions"}, []string{"Sample Kiosk"}},
		{"What subscriptions do I pay for?", chatReading{intent: "recurring"}, []string{"Sample Streaming"}},
		{"Which new merchants did I pay this month?", chatReading{intent: "new_payees", period: "this_month"}, []string{"Sample Kiosk"}},
		{"How much of my spending is uncategorized?", chatReading{intent: "uncategorized"}, []string{"€25.00"}},
		{"How much did I get back in refunds?", chatReading{intent: "refunds"}, []string{"€10.00"}},
		{"How much did I move between my accounts this month?", chatReading{intent: "transfers", period: "this_month"}, []string{"€500.00"}},
		{"What are my account balances?", chatReading{intent: "account_balances"}, []string{"Savings"}},
		{"What is my net worth?", chatReading{intent: "net_worth"}, nil},
		{"How is my portfolio doing?", chatReading{intent: "portfolio_value"}, nil},
		{"How did my portfolio change this month?", chatReading{intent: "portfolio_change", period: "this_month"}, nil},
		{"Which pie performs best?", chatReading{intent: "pie_performance", order: "most"}, nil},
		{"How much did I earn in dividends?", chatReading{intent: "dividends"}, nil},
		{"What needs my review?", chatReading{intent: "review_status"}, nil},
		{"When was my last import?", chatReading{intent: "imports_status"}, nil},
		{"What categories do I have?", chatReading{intent: "list_categories"}, []string{"Subscriptions"}},
		{"How many transactions do I have?", chatReading{intent: "data_overview"}, nil},
		{"What is the USD exchange rate?", chatReading{intent: "exchange_rate"}, nil},
		{"What can you answer?", chatReading{intent: "help"}, nil},
		{"Hello", chatReading{intent: "greeting"}, nil},
		{"Should I buy more stocks?", chatReading{intent: "unsupported"}, nil},
		{"Something vague", chatReading{intent: "spending_total", confidence: 0.1}, nil},
		{"How much did I spend at Nonexistent Shop?", chatReading{intent: "spending_total", payee: 0.9}, nil},
	}
	covered := map[string]bool{}
	for _, item := range cases {
		fake.readings[item.question] = item.reading
		covered[item.reading.intent] = true
		answer := app.ask(cookie, 0, item.question).Message.Answer
		if answer.Text == "" {
			t.Errorf("%q: empty answer", item.question)
		}
		if strings.ContainsAny(answer.Text+strings.Join(answer.Notes, ""), "—–") {
			t.Errorf("%q: dash in %q", item.question, answer.Text)
		}
		for _, want := range item.want {
			found := strings.Contains(answer.Text, want)
			if answer.Table != nil {
				encoded, _ := json.Marshal(answer.Table)
				found = found || strings.Contains(string(encoded), want)
			}
			if !found {
				t.Errorf("%q: want %q in %+v", item.question, want, answer)
			}
		}
	}
	for _, item := range assistant.Intents() {
		if item != "follow_up" && !covered[item] {
			t.Errorf("intent %s has no case", item)
		}
	}
}

func TestChatRanksPeriodsByValue(t *testing.T) {
	fake, client := newChatJev(t)
	app := newTestApp(t, Options{Integrations: client, Now: func() time.Time { return localTime("2026-09-20", 12, 0) }})
	cookie := app.setup()
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)
	chatLedger(app, cookie)

	spent := func(answer assistant.Answer) []float64 {
		var values []float64
		for _, row := range answer.Table.Rows {
			values = append(values, row[1].(float64))
		}
		return values
	}
	cases := []struct {
		question, order, want string
		sorted               func(a, b float64) bool
	}{
		{"In which month did I spend the most?", "most", "Your highest spending by month", func(a, b float64) bool { return a >= b }},
		{"In which month did I spend the least?", "least", "Your lowest spending by month", func(a, b float64) bool { return a <= b }},
	}
	for _, item := range cases {
		fake.readings[item.question] = chatReading{intent: "by_month", direction: "spending", order: item.order}
		answer := app.ask(cookie, 0, item.question).Message.Answer
		values := spent(answer)
		if !strings.HasPrefix(answer.Text, item.want) || len(values) < 2 {
			t.Fatalf("%q: %+v", item.question, answer)
		}
		for i := 1; i < len(values); i++ {
			if !item.sorted(values[i-1], values[i]) {
				t.Fatalf("%q: rows are not ranked by value: %v", item.question, values)
			}
		}
	}

	fake.readings["Show my spending by month"] = chatReading{intent: "by_month", direction: "spending"}
	answer := app.ask(cookie, 0, "Show my spending by month").Message.Answer
	var previous time.Time
	for _, row := range answer.Table.Rows {
		month, err := time.Parse("Jan 2006", row[0].(string))
		if err != nil || !month.After(previous) {
			t.Fatalf("a plain breakdown stays in date order: %v", answer.Table.Rows)
		}
		previous = month
	}
}
