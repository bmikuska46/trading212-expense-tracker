package httpserver

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	memberEmail     = "member@example.com"
	memberTemporary = "temporary password one"
	memberPassword  = "member chosen password"
)

func (app *testApp) signIn(email, password string) *http.Cookie {
	app.t.Helper()
	response := app.request("POST", "/api/session", map[string]string{"email": email, "password": password}, nil)
	if response.Code != http.StatusOK {
		app.t.Fatalf("sign in %s: %d %s", email, response.Code, response.Body)
	}
	return sessionCookie(app.t, response)
}

func TestOwnerAddsMemberWhoChoosesAPassword(t *testing.T) {
	app := newTestApp(t, Options{})
	owner := app.setup()

	if response := app.request("POST", "/api/users", map[string]string{"email": ownerEmail, "password": memberTemporary}, owner); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate email: %d %s", response.Code, response.Body)
	}
	created := app.request("POST", "/api/users", map[string]string{"email": memberEmail, "password": memberTemporary}, owner)
	if created.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", created.Code, created.Body)
	}
	member := decode[userEntry](t, created)
	if member.Owner || !member.MustChangePassword || member.Email != memberEmail {
		t.Fatalf("created user: %+v", member)
	}
	if _, err := os.Stat(filepath.Join(app.directory, "data", "users", strconv.FormatInt(member.ID, 10)+".db")); err != nil {
		t.Fatalf("member ledger: %v", err)
	}

	signIn := app.request("POST", "/api/session", map[string]string{"email": memberEmail, "password": memberTemporary}, nil)
	state := decode[sessionView](t, signIn)
	if state.User == nil || state.User.Owner || !state.User.MustChangePassword {
		t.Fatalf("member session: %s", signIn.Body)
	}
	temporary := sessionCookie(t, signIn)
	if response := app.request("GET", "/api/accounts", nil, temporary); response.Code != http.StatusForbidden {
		t.Fatalf("ledger before choosing a password: %d", response.Code)
	}
	if response := app.request("POST", "/api/password", map[string]string{"currentPassword": memberTemporary, "newPassword": memberTemporary}, temporary); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reusing the temporary password: %d", response.Code)
	}
	if response := app.request("POST", "/api/password", map[string]string{"currentPassword": "wrong password here", "newPassword": memberPassword}, temporary); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current password: %d", response.Code)
	}
	changed := app.request("POST", "/api/password", map[string]string{"currentPassword": memberTemporary, "newPassword": memberPassword}, temporary)
	if changed.Code != http.StatusOK || decode[sessionView](t, changed).User.MustChangePassword {
		t.Fatalf("change password: %d %s", changed.Code, changed.Body)
	}
	if response := app.request("GET", "/api/accounts", nil, temporary); response.Code != http.StatusOK {
		t.Fatalf("ledger after choosing a password: %d", response.Code)
	}

	// Each user sees only their own ledger.
	if response := app.request("POST", "/api/accounts", map[string]string{"name": "Member wallet", "currency": "EUR"}, temporary); response.Code != http.StatusCreated {
		t.Fatalf("member account: %d %s", response.Code, response.Body)
	}
	if accounts := decode[map[string][]accountView](t, app.request("GET", "/api/accounts", nil, owner))["accounts"]; len(accounts) != 0 {
		t.Fatalf("owner sees member accounts: %+v", accounts)
	}
	if response := app.request("GET", "/api/users", nil, temporary); response.Code != http.StatusForbidden {
		t.Fatalf("member listing users: %d", response.Code)
	}
	if response := app.request("POST", "/api/users", map[string]string{"email": "other@example.com", "password": memberTemporary}, temporary); response.Code != http.StatusForbidden {
		t.Fatalf("member adding users: %d", response.Code)
	}

	// The ledgers survive a restart.
	app.restart()
	cookie := app.signIn(memberEmail, memberPassword)
	if accounts := decode[map[string][]accountView](t, app.request("GET", "/api/accounts", nil, cookie))["accounts"]; len(accounts) != 1 || accounts[0].Name != "Member wallet" {
		t.Fatalf("member accounts after restart: %+v", accounts)
	}

	owner = app.signIn(ownerEmail, ownerPassword)
	users := decode[map[string][]userEntry](t, app.request("GET", "/api/users", nil, owner))["users"]
	if len(users) != 2 || !users[0].Owner || users[1].MustChangePassword {
		t.Fatalf("users: %+v", users)
	}
	path := "/api/users/" + strconv.FormatInt(member.ID, 10)
	if response := app.request("PUT", "/api/users/1/password", map[string]string{"password": memberTemporary}, owner); response.Code != http.StatusConflict {
		t.Fatalf("resetting the owner: %d", response.Code)
	}
	if response := app.request("PUT", path+"/password", map[string]string{"password": memberTemporary}, owner); response.Code != http.StatusOK {
		t.Fatalf("reset password: %d %s", response.Code, response.Body)
	}
	if response := app.request("GET", "/api/settings", nil, cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("a reset must sign the user out: %d", response.Code)
	}
	if state := decode[sessionView](t, app.request("POST", "/api/session", map[string]string{"email": memberEmail, "password": memberTemporary}, nil)); !state.User.MustChangePassword {
		t.Fatal("a reset must ask for a new password")
	}

	if response := app.request("DELETE", "/api/users/1", nil, owner); response.Code != http.StatusConflict {
		t.Fatalf("deleting the owner: %d", response.Code)
	}
	if response := app.request("DELETE", path, nil, owner); response.Code != http.StatusNoContent {
		t.Fatalf("delete user: %d", response.Code)
	}
	if response := app.request("DELETE", path, nil, owner); response.Code != http.StatusNotFound {
		t.Fatalf("deleting twice: %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(app.directory, "data", "users", strconv.FormatInt(member.ID, 10)+".db")); !os.IsNotExist(err) {
		t.Fatalf("the ledger of a deleted user remains: %v", err)
	}
	if response := app.request("POST", "/api/session", map[string]string{"email": memberEmail, "password": memberTemporary}, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user signing in: %d", response.Code)
	}
	again := decode[userEntry](t, app.request("POST", "/api/users", map[string]string{"email": memberEmail, "password": memberTemporary}, owner))
	if again.ID == member.ID {
		t.Fatal("a new user must not reuse the id of a deleted one")
	}
}

func TestMembersShareTypeSafeAndResend(t *testing.T) {
	_, client := newFakeServices(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Integrations: client, Now: func() time.Time { return now }})
	owner := app.setup()
	if response := app.request("PUT", "/api/integrations/typesafe", map[string]any{"apiKey": syntheticKey, "enabled": true}, owner); response.Code != http.StatusOK {
		t.Fatalf("TypeSafe: %d %s", response.Code, response.Body)
	}
	resend := map[string]any{"apiKey": syntheticKey, "sender": "Budget <budget@example.com>", "recipient": ownerEmail,
		"applicationUrl": "https://budget.home.arpa", "enabled": true}
	if response := app.request("PUT", "/api/integrations/resend", resend, owner); response.Code != http.StatusOK {
		t.Fatalf("Resend: %d %s", response.Code, response.Body)
	}
	app.request("POST", "/api/users", map[string]string{"email": memberEmail, "password": memberTemporary}, owner)
	member := app.signIn(memberEmail, memberTemporary)
	app.request("POST", "/api/password", map[string]string{"currentPassword": memberTemporary, "newPassword": memberPassword}, member)

	listed := app.request("GET", "/api/integrations", nil, member)
	assertNoSecrets(t, listed)
	views := map[string]integrationView{}
	for _, view := range decode[map[string][]integrationView](t, listed)["integrations"] {
		views[view.Provider] = view
	}
	if views["trading212"].Configured || !views["typesafe"].Enabled || views["typesafe"].APIKeyHint != "" ||
		!views["resend"].Enabled || views["resend"].Recipient != memberEmail {
		t.Fatalf("member integrations: %+v", views)
	}
	for _, request := range []struct{ method, path string }{
		{"PUT", "/api/integrations/typesafe"}, {"DELETE", "/api/integrations/typesafe"}, {"POST", "/api/integrations/typesafe/check"},
		{"PUT", "/api/integrations/resend"}, {"DELETE", "/api/integrations/resend"},
	} {
		if response := app.request(request.method, request.path, resend, member); response.Code != http.StatusForbidden {
			t.Fatalf("member %s %s: %d", request.method, request.path, response.Code)
		}
	}

	trading := map[string]any{"environment": "demo", "apiKey": syntheticKey, "apiSecret": syntheticSecret, "accountName": "Member broker", "enabled": true}
	if response := app.request("PUT", "/api/integrations/trading212", trading, member); response.Code != http.StatusOK {
		t.Fatalf("member Trading 212: %d %s", response.Code, response.Body)
	}
	for _, view := range decode[map[string][]integrationView](t, app.request("GET", "/api/integrations", nil, owner))["integrations"] {
		if view.Provider == "trading212" && view.Configured {
			t.Fatal("the member's Trading 212 key leaked into the owner's ledger")
		}
	}
}
