package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
)

// categoryJev answers category questions by merchant name. Merchants it
// does not know get "none".
type categoryJev struct {
	mu        sync.Mutex
	bodies    [][]byte
	merchants [][]string
	picks     map[string]string
	certainty map[string]float64
	failing   atomic.Bool
}

func newCategoryJev(t *testing.T) (*categoryJev, *integrations.Client) {
	fake := &categoryJev{picks: map[string]string{}, certainty: map[string]float64{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/typesafe/models":
			w.Write([]byte(`{"models":[{"name":"` + integrations.JevModel + `"}]}`))
		case "/typesafe/systemone":
			body, _ := io.ReadAll(r.Body)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.bodies = append(fake.bodies, body)
			if fake.failing.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var request struct {
				Questions map[string]struct {
					Type         string `json:"type"`
					Instructions struct {
						Merchant string `json:"merchant"`
					} `json:"instructions"`
					Criteria map[string]any `json:"criteria"`
				} `json:"questions"`
			}
			if json.Unmarshal(body, &request) != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			answers := map[string]integrations.Answer{}
			var merchants []string
			for id, question := range request.Questions {
				merchant := question.Instructions.Merchant
				merchants = append(merchants, merchant)
				picked := "none"
				for key, name := range question.Criteria {
					if name == fake.picks[merchant] {
						picked = key
					}
				}
				confidence := 0.9
				if value, ok := fake.certainty[merchant]; ok {
					confidence = value
				}
				answers[id] = integrations.Answer{Type: question.Type, Choice: picked,
					Probabilities: map[string]float64{picked: confidence}, Confidence: confidence}
			}
			fake.merchants = append(fake.merchants, merchants)
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

func (f *categoryJev) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

const notedFile = "Date;Payee;Out;In;Note\n" +
	"02.08.2026;Example Bistro;12,40;;Lunch near the office\n" +
	"03.08.2026;Salary Example;;1 500,00;August salary\n" +
	"04.08.2026;Example Market;54,99;;\n" +
	"05.08.2026;Sample Kiosk;8,10;;\n" +
	"06.08.2026;Example Bistro;9,80;;Dinner\n"

func notedMapping() imports.Mapping {
	column := func(index int) *int { return &index }
	return imports.Mapping{
		Date: imports.DateField{Column: 0, Format: imports.DateDMY, Timezone: imports.ZoneLocal},
		Amount: imports.AmountField{Mode: imports.AmountDebitCredit, DebitColumn: column(2), CreditColumn: column(3),
			Sign: imports.NegativeIsOutflow, DecimalSeparator: ",", ThousandsSeparator: " "},
		Currency:    imports.CurrencyField{Fixed: new("EUR")},
		PayeeColumn: column(1), NotesColumn: column(4),
		Actions: []imports.ActionRule{}, Ambiguities: []string{},
	}
}

func (app *testApp) mapped(cookie *http.Cookie, account int64, filename, body string) int64 {
	app.t.Helper()
	staged := app.imported(cookie, account, filename, []byte(body))
	ready := decode[importDetail](app.t, app.expect(http.StatusOK, "PUT", fmt.Sprintf("/api/imports/%d/mapping", staged.ID),
		map[string]any{"mapping": notedMapping()}, cookie))
	if ready.State != stateReady {
		app.t.Fatalf("mapping: %+v", ready)
	}
	return staged.ID
}

func (app *testApp) ruleCategory(payee string) int64 {
	app.t.Helper()
	var category int64
	err := app.store.DB.QueryRow(`SELECT coalesce(max(r.category_id), 0) FROM categorization_rules r
		JOIN payees p ON p.id = r.payee_id WHERE p.name = ?`, payee).Scan(&category)
	if err != nil {
		app.t.Fatal(err)
	}
	return category
}

func TestJevCategorizesNewPayees(t *testing.T) {
	fake, client := newCategoryJev(t)
	app := newTestApp(t, Options{Integrations: client})
	cookie := app.setup()
	account := app.account(cookie, "Bank")
	dining := app.category(cookie, "Dining")
	app.category(cookie, "Groceries")
	fake.picks = map[string]string{"Example Bistro": "Dining", "Example Market": "Groceries", "Sample Kiosk": "Dining"}
	fake.certainty["Sample Kiosk"] = 0.3

	// Mapping suggestions alone never ask about categories.
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)
	off := app.commit(cookie, app.mapped(cookie, account, "off.csv", "Date;Payee;Out;In;Note\n01.08.2026;Example Bistro;1,00;;\n"))
	if fake.requests() != 0 || off.SuggestedCategoryCount != 0 || off.ReviewStatus != "confirmed" {
		t.Fatalf("categories must not be asked about while the switch is off: %d %+v", fake.requests(), off)
	}

	saved := decode[integrationView](t, app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe",
		map[string]any{"apiKey": "", "enabled": false, "categorize": true}, cookie))
	if !saved.Categorize || saved.Enabled || !saved.Configured {
		t.Fatalf("categorize switch: %+v", saved)
	}

	// Example Bistro now has a payee but no rule, so it is asked about.
	batch := app.mapped(cookie, account, "august.csv", notedFile)
	committed := app.commit(cookie, batch)
	if fake.requests() != 1 {
		t.Fatalf("one request for the batch: %d", fake.requests())
	}
	body := fake.bodies[0]
	for _, private := range []string{"12,40", "1 500", "54,99", "08.2026", "Bank"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("the request must hold only payees, directions, and notes, found %q: %s", private, body)
		}
	}
	if len(fake.merchants[0]) != 4 || !bytes.Contains(body, []byte(`"Lunch near the office"`)) || bytes.Contains(body, []byte("Dinner")) ||
		!bytes.Contains(body, []byte(`"money in"`)) || bytes.Contains(body, []byte("Uncategorized")) {
		t.Fatalf("one question per distinct payee with its first notes: %s", body)
	}
	if committed.SuggestedCategoryCount != 3 || committed.ReviewStatus != "needs_review" {
		t.Fatalf("picks must wait for review: %+v", committed)
	}
	if notifications := app.notifications(cookie); len(notifications) != 0 {
		t.Fatalf("a manual commit creates no notification: %+v", notifications)
	}
	rows := app.batchTransactions(cookie, batch)
	category := func(payee string) string { return byNotesOrPayee(rows, payee).CategoryName }
	if category("Example Bistro") != "Dining" || category("Example Market") != "Groceries" ||
		category("Sample Kiosk") != "Uncategorized" || category("Salary Example") != "Uncategorized" {
		t.Fatalf("categories: %+v", rows)
	}

	// The owner corrects the market, then confirms; kept and corrected picks
	// become rules, and nothing else does.
	market := byNotesOrPayee(rows, "Example Market")
	app.expect(http.StatusOK, "PATCH", fmt.Sprintf("/api/transactions/%d", market.ID), editInput(market, map[string]any{"categoryId": dining}), cookie)
	app.expect(http.StatusOK, "POST", fmt.Sprintf("/api/imports/%d/confirm", batch), nil, cookie)
	if app.ruleCategory("Example Bistro") != dining || app.ruleCategory("Example Market") != dining ||
		app.ruleCategory("Sample Kiosk") != 0 || app.ruleCategory("Salary Example") != 0 {
		t.Fatalf("rules after confirmation: bistro %d market %d", app.ruleCategory("Example Bistro"), app.ruleCategory("Example Market"))
	}

	// The monthly path asks only about payees still without a rule.
	next := app.mapped(cookie, account, "september.csv", "Date;Payee;Out;In;Note\n"+
		"02.09.2026;Example Bistro;11,00;;\n03.09.2026;Sample Kiosk;2,00;;\n04.09.2026;Example Pharmacy;7,00;;\n")
	fake.picks["Example Pharmacy"] = "Groceries"
	unreviewed := app.commitUnreviewed(cookie, next)
	if fake.requests() != 2 || strings.Join(slices.Sorted(slices.Values(fake.merchants[1])), ",") != "Example Pharmacy,Sample Kiosk" {
		t.Fatalf("payees with a rule must not be asked about again: %v", fake.merchants)
	}
	if unreviewed.SuggestedCategoryCount != 1 || unreviewed.ReviewStatus != "needs_review" {
		t.Fatalf("monthly commit: %+v", unreviewed)
	}
	rows = app.batchTransactions(cookie, next)
	if byNotesOrPayee(rows, "Example Bistro").CategoryName != "Dining" || byNotesOrPayee(rows, "Example Pharmacy").CategoryName != "Groceries" {
		t.Fatalf("rules and picks: %+v", rows)
	}

	// A failing request never blocks the commit; rows stay Uncategorized and
	// a manual commit confirms as before.
	fake.failing.Store(true)
	failed := app.commit(cookie, app.mapped(cookie, account, "october.csv", "Date;Payee;Out;In;Note\n02.10.2026;Example Garage;30,00;;\n"))
	if failed.SuggestedCategoryCount != 0 || failed.ReviewStatus != "confirmed" || failed.InsertedCount != 1 {
		t.Fatalf("commit with TypeSafe down: %+v", failed)
	}
}

func TestJevCategorizesLedger(t *testing.T) {
	fake, client := newCategoryJev(t)
	app := newTestApp(t, Options{Integrations: client})
	cookie := app.setup()
	wallet := app.account(cookie, "Wallet")
	shopping := app.category(cookie, "Shopping")
	expense := func(payee string, extra map[string]any) transactionView {
		entry := map[string]any{"accountId": wallet, "date": "2026-09-03", "kind": "expense", "amount": "4", "payee": payee}
		maps.Copy(entry, extra)
		return app.transaction(cookie, entry)
	}
	lunch := expense("Example Bistro", map[string]any{"notes": "Lunch"})
	dinner := expense("Example Bistro", nil)
	kiosk := expense("Sample Kiosk", nil)
	ruled := expense("Example Pharmacy", nil)
	chosen := expense("Example Shop", map[string]any{"categoryId": shopping})
	split := expense("Example Market", map[string]any{"splits": []map[string]any{
		{"categoryId": shopping, "amount": "3"}, {"categoryId": shopping, "amount": "1"}}})
	if lunch.CategoryName != "Uncategorized" || ruled.CategoryName != "Uncategorized" {
		t.Fatalf("new payees start Uncategorized: %+v %+v", lunch, ruled)
	}

	app.expect(http.StatusConflict, "POST", "/api/transactions/categorize", nil, cookie)
	app.expect(http.StatusOK, "PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, cookie)
	health := app.category(cookie, "Health")
	app.expect(http.StatusOK, "POST", "/api/rules", map[string]any{"payeeId": *ruled.PayeeID, "categoryId": health}, cookie)
	fake.picks = map[string]string{"Example Bistro": "Eating out", "Sample Kiosk": "Groceries"}
	fake.certainty["Sample Kiosk"] = 0.3

	result := decode[map[string]int](t, app.expect(http.StatusOK, "POST", "/api/transactions/categorize", nil, cookie))
	if result["categorized"] != 3 || result["remaining"] != 1 {
		t.Fatalf("result: %+v", result)
	}
	// One question per payee; the ruled, chosen, and split payees are not asked about.
	if fake.requests() != 1 || strings.Join(slices.Sorted(slices.Values(fake.merchants[0])), ",") != "Example Bistro,Sample Kiosk" {
		t.Fatalf("questions: %v", fake.merchants)
	}
	if !bytes.Contains(fake.bodies[0], []byte(`"Lunch"`)) || bytes.Contains(fake.bodies[0], []byte("Wallet")) {
		t.Fatalf("the request holds the latest notes and no account: %s", fake.bodies[0])
	}
	for transaction, want := range map[int64]string{lunch.ID: "Eating out", dinner.ID: "Eating out", kiosk.ID: "Uncategorized",
		ruled.ID: "Health", chosen.ID: "Shopping"} {
		if detail := app.detail(cookie, transaction); detail.CategoryName != want {
			t.Fatalf("transaction %d: %q, want %q", transaction, detail.CategoryName, want)
		}
	}
	if detail := app.detail(cookie, split.ID); detail.CategoryID != nil || detail.Revision != split.Revision {
		t.Fatalf("splits keep their parts: %+v", detail)
	}

	// TypeSafe failing with nothing to apply is reported.
	fake.failing.Store(true)
	app.expect(http.StatusBadGateway, "POST", "/api/transactions/categorize", nil, cookie)
}
