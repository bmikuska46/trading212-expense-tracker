package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
)

const (
	trading212 = "trading212"
	typeSafe   = "typesafe"
	resend     = "resend"
	// Checks call up to three endpoints with their own timeouts.
	checkTimeout = 35 * time.Second
)

var providers = []string{trading212, typeSafe, resend}

// shared reports whether all users share one setting of the provider, which
// the main database keeps and only the owner changes. Every user connects
// their own Trading 212 account.
func shared(provider string) bool { return provider == typeSafe || provider == resend }

func (a *api) settingsStore(provider string) *database.Store {
	if shared(provider) {
		return a.main
	}
	return a.store
}

// storedCredentials is the credentials_json document. It never leaves the server.
type storedCredentials struct {
	Environment string `json:"environment,omitempty"`
	APIKey      string `json:"apiKey,omitempty"`
	APISecret   string `json:"apiSecret,omitempty"`
	// Categorize turns on TypeSafe category suggestions for new payees at
	// import commit. The imports package reads the same field.
	Categorize bool `json:"categorize,omitempty"`
	// Sync selects what the Trading 212 jobs read; a key saved before it
	// existed reads everything. The jobs package reads the same field.
	Sync *integrations.Trading212Sync `json:"sync,omitempty"`
}

type accountRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

// integrationView is safe to send to the browser: secrets are reduced to
// whether they are configured plus the last four characters of the key.
type integrationView struct {
	Provider                string                       `json:"provider"`
	Configured              bool                         `json:"configured"`
	Enabled                 bool                         `json:"enabled"`
	APIKeyHint              string                       `json:"apiKeyHint"`
	APISecretConfigured     bool                         `json:"apiSecretConfigured"`
	Environment             string                       `json:"environment,omitempty"`
	Account                 *accountRef                  `json:"account"`
	EarliestAutomaticPeriod string                       `json:"earliestAutomaticPeriod"`
	Sender                  string                       `json:"sender"`
	Recipient               string                       `json:"recipient"`
	ApplicationURL          string                       `json:"applicationUrl"`
	Check                   *integrations.Check          `json:"check"`
	CheckedAt               string                       `json:"checkedAt"`
	Categorize              bool                         `json:"categorize"`
	Sync                    *integrations.Trading212Sync `json:"sync,omitempty"`
}

type checkError struct {
	Error string             `json:"error"`
	Check integrations.Check `json:"check"`
}

func knownProvider(provider string) bool {
	for _, known := range providers {
		if provider == known {
			return true
		}
	}
	return false
}

func keyHint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return "…" + key[len(key)-4:]
}

// validSecret accepts printable ASCII without spaces, which covers the key
// formats of all three services and keeps control characters out of headers.
func validSecret(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func mergeSecret(input, stored string) string {
	if value := strings.TrimSpace(input); value != "" {
		return value
	}
	return stored
}

func (a *api) loadIntegration(ctx context.Context, queries *dbgen.Queries, provider string) (dbgen.Integration, storedCredentials, bool, error) {
	row, err := queries.GetIntegration(ctx, provider)
	if errors.Is(err, sql.ErrNoRows) {
		return dbgen.Integration{Provider: provider}, storedCredentials{}, false, nil
	}
	if err != nil {
		return row, storedCredentials{}, false, err
	}
	var credentials storedCredentials
	if err := json.Unmarshal([]byte(row.CredentialsJson), &credentials); err != nil {
		return row, storedCredentials{}, false, err
	}
	return row, credentials, true, nil
}

func (a *api) integrationView(ctx context.Context, current *session, provider string) (integrationView, error) {
	row, credentials, _, err := a.loadIntegration(ctx, a.settingsStore(provider).Queries, provider)
	if err != nil {
		return integrationView{}, err
	}
	view := integrationView{
		Provider: provider, Configured: credentials.APIKey != "", Enabled: row.Enabled == 1,
		APIKeyHint: keyHint(credentials.APIKey), CheckedAt: row.CheckedAt.String,
		EarliestAutomaticPeriod: row.EarliestAutomaticPeriod.String,
	}
	if row.CheckJson.Valid {
		var check integrations.Check
		if json.Unmarshal([]byte(row.CheckJson.String), &check) == nil {
			view.Check = &check
		}
	}
	switch provider {
	case trading212:
		view.Environment = credentials.Environment
		if view.Environment == "" {
			view.Environment = "live"
		}
		view.APISecretConfigured = credentials.APISecret != ""
		sync := integrations.Trading212SyncOrAll(credentials.Sync)
		view.Sync = &sync
		if row.SourceAccountID.Valid {
			account, err := a.store.Queries.GetAccount(ctx, row.SourceAccountID.Int64)
			if err != nil {
				return view, err
			}
			view.Account = &accountRef{ID: account.ID, Name: account.Name, Currency: account.Currency}
		}
	case resend:
		config, err := a.main.Queries.GetNotificationConfig(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return view, err
		}
		view.Sender, view.Recipient, view.ApplicationURL = config.Sender, config.Recipient, config.ApplicationUrl
		if !current.owner() && view.Recipient != "" {
			view.Recipient = current.email
		}
	case typeSafe:
		view.Categorize = credentials.Categorize
	}
	// Other users see whether a shared key works, but not any of it.
	if shared(provider) && !current.owner() {
		view.APIKeyHint = ""
	}
	return view, nil
}

func (a *api) writeIntegration(w http.ResponseWriter, r *http.Request, current *session, provider string) {
	view, err := a.integrationView(r.Context(), current, provider)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) listIntegrations(w http.ResponseWriter, r *http.Request, current *session) {
	views := make([]integrationView, 0, len(providers))
	for _, provider := range providers {
		view, err := a.integrationView(r.Context(), current, provider)
		if err != nil {
			internalError(w, "load integrations")
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string][]integrationView{"integrations": views})
}

// rejectCheck stops a save when the credentials do not work, or when the owner
// asks to turn on an integration whose required permissions are missing.
func rejectCheck(w http.ResponseWriter, check integrations.Check, enabled bool) bool {
	switch {
	case check.Status == integrations.StatusFailed:
		writeJSON(w, http.StatusUnprocessableEntity, checkError{Error: check.Message, Check: check})
	case enabled && check.Status == integrations.StatusPartial:
		writeJSON(w, http.StatusUnprocessableEntity, checkError{
			Error: "Resolve the missing items, or turn off what needs them before saving.", Check: check,
		})
	default:
		return false
	}
	return true
}

// missesNeeded reports whether a Trading 212 permission that a selected part
// uses is missing, so a key without, say, dividend history can still be
// saved with dividends turned off.
func missesNeeded(check integrations.Check, monthly bool, sync integrations.Trading212Sync) bool {
	for _, item := range check.Items {
		if item.State == integrations.StateMissing && integrations.Trading212Needs(item.Key, monthly, sync) {
			return true
		}
	}
	return false
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// ownsSetting rejects a change to a shared setting by anyone but the owner.
func ownsSetting(w http.ResponseWriter, current *session, provider string) bool {
	if shared(provider) && !current.owner() {
		writeError(w, http.StatusForbidden, "Only the owner can change this integration")
		return false
	}
	return true
}

func (a *api) saveIntegration(w http.ResponseWriter, r *http.Request, current *session) {
	provider := r.PathValue("provider")
	if !ownsSetting(w, current, provider) {
		return
	}
	switch provider {
	case trading212:
		a.saveTrading212(w, r, current)
	case typeSafe:
		a.saveTypeSafe(w, r, current)
	case resend:
		a.saveResend(w, r, current)
	default:
		writeError(w, http.StatusNotFound, "Unknown integration")
	}
}

func (a *api) saveTrading212(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		Environment string `json:"environment"`
		APIKey      string `json:"apiKey"`
		APISecret   string `json:"apiSecret"`
		AccountName string `json:"accountName"`
		Enabled     bool   `json:"enabled"`
		// Sync is optional; without it the saved selection is kept.
		Sync *integrations.Trading212Sync `json:"sync"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	_, stored, _, err := a.loadIntegration(r.Context(), a.store.Queries, trading212)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	credentials := storedCredentials{
		Environment: input.Environment, APIKey: mergeSecret(input.APIKey, stored.APIKey),
		APISecret: mergeSecret(input.APISecret, stored.APISecret), Sync: stored.Sync,
	}
	if input.Sync != nil {
		sync := integrations.Trading212SyncOrAll(input.Sync)
		credentials.Sync = &sync
	}
	accountName := strings.TrimSpace(input.AccountName)
	fields := map[string]string{}
	if credentials.Environment != "live" && credentials.Environment != "demo" {
		fields["environment"] = "Choose live or demo"
	}
	if !validSecret(credentials.APIKey) {
		fields["apiKey"] = "Enter the API key"
	}
	if !validSecret(credentials.APISecret) {
		fields["apiSecret"] = "Enter the API secret"
	}
	if utf8.RuneCountInString(accountName) > 100 {
		fields["accountName"] = "Use at most 100 characters"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	// Call the service before opening a transaction, so the single database
	// connection is never held during network requests.
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	check, account := a.integrations.CheckTrading212(ctx, integrations.Trading212Credentials{
		Environment: credentials.Environment, APIKey: credentials.APIKey, APISecret: credentials.APISecret,
	})
	cancel()
	if rejectCheck(w, check, missesNeeded(check, input.Enabled, integrations.Trading212SyncOrAll(credentials.Sync))) {
		return
	}
	checkJSON, _ := json.Marshal(check)
	credentialsJSON, _ := json.Marshal(credentials)
	location, err := time.LoadLocation(current.timezone)
	if err != nil {
		location, _ = time.LoadLocation(auth.DefaultTimezone)
	}
	now := a.now()
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin integration save")
		return
	}
	defer tx.Rollback()
	queries := a.store.Queries.WithTx(tx)
	previous, _, _, err := a.loadIntegration(r.Context(), queries, trading212)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	// Automatic imports start with the month in which they are turned on and
	// never request older history.
	earliest := previous.EarliestAutomaticPeriod
	if input.Enabled && (previous.Enabled == 0 || !earliest.Valid) {
		earliest = nullString(now.In(location).Format("2006-01"))
	}
	params := dbgen.SaveIntegrationParams{
		Provider: trading212, CredentialsJson: string(credentialsJSON), Enabled: boolInt(input.Enabled),
		EarliestAutomaticPeriod: earliest, SourceAccountID: previous.SourceAccountID,
		CheckJson: nullString(string(checkJSON)), CheckedAt: nullString(auth.FormatTime(now)),
	}
	// The integration row must exist before an account can reference it.
	if err := queries.SaveIntegration(r.Context(), params); err != nil {
		internalError(w, "save integration")
		return
	}
	linked, err := queries.FindIntegrationAccount(r.Context(), dbgen.FindIntegrationAccountParams{
		IntegrationProvider: nullString(trading212), ExternalAccountID: nullString(account.ID),
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if accountName == "" {
			accountName = "Trading 212"
		}
		linked, err = queries.CreateAccount(r.Context(), dbgen.CreateAccountParams{
			Name: accountName, Currency: account.Currency,
			IntegrationProvider: nullString(trading212), ExternalAccountID: nullString(account.ID),
		})
		if err != nil {
			internalError(w, "create source account")
			return
		}
	case err != nil:
		internalError(w, "find source account")
		return
	case linked.Currency != account.Currency:
		writeError(w, http.StatusConflict, "Trading 212 reports a different currency for this account than the one already recorded.")
		return
	case accountName != "" && accountName != linked.Name:
		if err := queries.RenameAccount(r.Context(), dbgen.RenameAccountParams{Name: accountName, ID: linked.ID}); err != nil {
			internalError(w, "rename source account")
			return
		}
	}
	params.SourceAccountID = sql.NullInt64{Int64: linked.ID, Valid: true}
	if err := queries.SaveIntegration(r.Context(), params); err != nil {
		internalError(w, "link source account")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit integration")
		return
	}
	a.wakeInvesting()
	a.writeIntegration(w, r, current, trading212)
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func (a *api) saveTypeSafe(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		APIKey     string `json:"apiKey"`
		Enabled    bool   `json:"enabled"`
		Categorize bool   `json:"categorize"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	previous, stored, _, err := a.loadIntegration(r.Context(), a.main.Queries, typeSafe)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	credentials := storedCredentials{APIKey: mergeSecret(input.APIKey, stored.APIKey), Categorize: input.Categorize}
	if !validSecret(credentials.APIKey) {
		writeFieldErrors(w, map[string]string{"apiKey": "Enter the API key"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	check := a.integrations.CheckTypeSafe(ctx, credentials.APIKey)
	cancel()
	if rejectCheck(w, check, input.Enabled || input.Categorize) {
		return
	}
	checkJSON, _ := json.Marshal(check)
	credentialsJSON, _ := json.Marshal(credentials)
	if err := a.main.Queries.SaveIntegration(r.Context(), dbgen.SaveIntegrationParams{
		Provider: typeSafe, CredentialsJson: string(credentialsJSON), Enabled: boolInt(input.Enabled),
		EarliestAutomaticPeriod: previous.EarliestAutomaticPeriod, SourceAccountID: previous.SourceAccountID,
		CheckJson: nullString(string(checkJSON)), CheckedAt: nullString(auth.FormatTime(a.now())),
	}); err != nil {
		internalError(w, "save integration")
		return
	}
	a.writeIntegration(w, r, current, typeSafe)
}

// applicationURL accepts the absolute address the owner uses to open the app.
func applicationURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || len(value) > 2048 || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", false
	}
	return strings.TrimRight(parsed.String(), "/"), true
}

func senderDomain(sender string) (string, bool) {
	address, err := mail.ParseAddress(strings.TrimSpace(sender))
	if err != nil || len(sender) > 320 {
		return "", false
	}
	at := strings.LastIndex(address.Address, "@")
	domain := address.Address[at+1:]
	return domain, at > 0 && strings.Contains(domain, ".")
}

func (a *api) saveResend(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		APIKey         string `json:"apiKey"`
		Sender         string `json:"sender"`
		Recipient      string `json:"recipient"`
		ApplicationURL string `json:"applicationUrl"`
		Enabled        bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	previous, stored, _, err := a.loadIntegration(r.Context(), a.main.Queries, resend)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	credentials := storedCredentials{APIKey: mergeSecret(input.APIKey, stored.APIKey)}
	fields := map[string]string{}
	if !validSecret(credentials.APIKey) {
		fields["apiKey"] = "Enter the API key"
	}
	domain, ok := senderDomain(input.Sender)
	if !ok {
		fields["sender"] = "Enter a sender such as Budget <budget@example.com>"
	}
	recipient, ok := auth.NormalizeEmail(input.Recipient)
	if !ok {
		fields["recipient"] = "Enter a single email address"
	}
	address, ok := applicationURL(input.ApplicationURL)
	if !ok {
		fields["applicationUrl"] = "Enter the full address you use to open this app, such as https://budget.home.arpa"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	check := a.integrations.CheckResend(ctx, credentials.APIKey, domain)
	cancel()
	if rejectCheck(w, check, input.Enabled) {
		return
	}
	checkJSON, _ := json.Marshal(check)
	credentialsJSON, _ := json.Marshal(credentials)
	tx, err := a.main.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin integration save")
		return
	}
	defer tx.Rollback()
	queries := a.main.Queries.WithTx(tx)
	if err := queries.SaveIntegration(r.Context(), dbgen.SaveIntegrationParams{
		Provider: resend, CredentialsJson: string(credentialsJSON), Enabled: boolInt(input.Enabled),
		EarliestAutomaticPeriod: previous.EarliestAutomaticPeriod, SourceAccountID: previous.SourceAccountID,
		CheckJson: nullString(string(checkJSON)), CheckedAt: nullString(auth.FormatTime(a.now())),
	}); err != nil {
		internalError(w, "save integration")
		return
	}
	if err := queries.SaveNotificationConfig(r.Context(), dbgen.SaveNotificationConfigParams{
		Sender: strings.TrimSpace(input.Sender), Recipient: recipient, ApplicationUrl: address, Enabled: boolInt(input.Enabled),
	}); err != nil {
		internalError(w, "save notification settings")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit integration")
		return
	}
	a.writeIntegration(w, r, current, resend)
}

// checkIntegration re-runs the connection check with stored credentials.
func (a *api) checkIntegration(w http.ResponseWriter, r *http.Request, current *session) {
	provider := r.PathValue("provider")
	if !knownProvider(provider) {
		writeError(w, http.StatusNotFound, "Unknown integration")
		return
	}
	if !ownsSetting(w, current, provider) {
		return
	}
	settings := a.settingsStore(provider)
	row, credentials, _, err := a.loadIntegration(r.Context(), settings.Queries, provider)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	if credentials.APIKey == "" {
		writeError(w, http.StatusConflict, "Save this integration before checking it")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()
	var check integrations.Check
	switch provider {
	case trading212:
		var account *integrations.Trading212Account
		check, account = a.integrations.CheckTrading212(ctx, integrations.Trading212Credentials{
			Environment: credentials.Environment, APIKey: credentials.APIKey, APISecret: credentials.APISecret,
		})
		if account != nil && row.SourceAccountID.Valid {
			linked, err := a.store.Queries.GetAccount(r.Context(), row.SourceAccountID.Int64)
			if err != nil {
				internalError(w, "load source account")
				return
			}
			if linked.ExternalAccountID.String != account.ID {
				check.Items = append(check.Items, integrations.Item{
					Key: "linked_account", Label: "Linked source account", State: integrations.StateMissing,
					Detail: "The key belongs to a different Trading 212 account; save the integration again to link it",
				})
				check.Status, check.Message = integrations.StatusPartial, "Connected, but some permissions are missing"
			}
		}
	case typeSafe:
		check = a.integrations.CheckTypeSafe(ctx, credentials.APIKey)
	case resend:
		config, err := a.main.Queries.GetNotificationConfig(r.Context())
		if err != nil {
			internalError(w, "load notification settings")
			return
		}
		domain, _ := senderDomain(config.Sender)
		check = a.integrations.CheckResend(ctx, credentials.APIKey, domain)
	}
	checkJSON, _ := json.Marshal(check)
	if err := settings.Queries.RecordIntegrationCheck(r.Context(), dbgen.RecordIntegrationCheckParams{
		CheckJson: nullString(string(checkJSON)), CheckedAt: nullString(auth.FormatTime(a.now())), Provider: provider,
	}); err != nil {
		internalError(w, "record check")
		return
	}
	a.writeIntegration(w, r, current, provider)
}

// removeIntegration clears credentials and turns the integration off. Linked
// accounts and their history stay in the ledger.
func (a *api) removeIntegration(w http.ResponseWriter, r *http.Request, current *session) {
	provider := r.PathValue("provider")
	if !knownProvider(provider) {
		writeError(w, http.StatusNotFound, "Unknown integration")
		return
	}
	if !ownsSetting(w, current, provider) {
		return
	}
	settings := a.settingsStore(provider)
	tx, err := settings.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin integration removal")
		return
	}
	defer tx.Rollback()
	queries := settings.Queries.WithTx(tx)
	row, _, exists, err := a.loadIntegration(r.Context(), queries, provider)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	if exists {
		if err := queries.SaveIntegration(r.Context(), dbgen.SaveIntegrationParams{
			Provider: provider, CredentialsJson: "{}", Enabled: 0,
			EarliestAutomaticPeriod: row.EarliestAutomaticPeriod, SourceAccountID: row.SourceAccountID,
		}); err != nil {
			internalError(w, "clear integration")
			return
		}
		if provider == resend {
			if err := queries.DisableNotifications(r.Context()); err != nil {
				internalError(w, "disable notifications")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit integration removal")
		return
	}
	if provider == trading212 {
		a.wakeInvesting()
	}
	a.writeIntegration(w, r, current, provider)
}
