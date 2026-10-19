package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path"
	"strings"
	"time"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/tenants"
)

type Status struct {
	Status        string `json:"status"`
	Database      string `json:"database"`
	CipherVersion string `json:"cipherVersion"`
	CreatedAt     string `json:"createdAt"`
}

type Options struct {
	// CookieSecure is "auto", "always", or "never"; see config.Config.
	CookieSecure     string
	Integrations     *integrations.Client
	Now              func() time.Time
	Turnstile        *auth.Turnstile
	TurnstileSiteKey string
	TrustedProxies   []netip.Prefix
	// ImportCommitHook runs after each row a commit inserts; tests use it to
	// interrupt the final transaction.
	ImportCommitHook func(inserted int) error
	// Imports is the pipeline shared with the monthly import job, so one
	// import step runs at a time. New creates one when it is nil.
	Imports *imports.Pipeline
	// WakeMonthly asks the monthly import job to run a rerun right away.
	WakeMonthly func()
	// WakeInvesting asks the snapshot and income jobs to look at a changed
	// Trading 212 key.
	WakeInvesting func()
	// Tenants keeps every user's ledger. New makes one without background
	// jobs when it is nil, with the owner's tenant built from the options
	// above.
	Tenants *tenants.Registry
}

type api struct {
	// store is the ledger of the signed-in user, and main the database with
	// sign-ins and the settings all users share. They are the same for the
	// owner and outside of a request.
	store            *database.Store
	main             *database.Store
	tenants          *tenants.Registry
	cookieSecure     string
	integrations     *integrations.Client
	imports          *imports.Pipeline
	wakeMonthly      func()
	wakeInvesting    func()
	now              func() time.Time
	throttle         *auth.Throttle
	turnstile        *auth.Turnstile
	turnstileSiteKey string
	loginLimiter     *auth.LoginLimiter
	trustedProxies   []netip.Prefix
}

func New(store *database.Store, webDirectory string, options Options) (http.Handler, error) {
	assets := os.DirFS(webDirectory)
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return nil, err
	}
	a := &api{store: store, main: store, cookieSecure: options.CookieSecure, integrations: options.Integrations, now: options.Now,
		throttle: &auth.Throttle{}, turnstile: options.Turnstile, turnstileSiteKey: options.TurnstileSiteKey,
		loginLimiter: auth.NewLoginLimiter(), trustedProxies: options.TrustedProxies}
	if a.cookieSecure == "" {
		a.cookieSecure = "auto"
	}
	if a.integrations == nil {
		a.integrations = integrations.NewClient()
	}
	if a.now == nil {
		a.now = time.Now
	}
	a.imports = options.Imports
	if a.imports == nil {
		a.imports = imports.New(store)
		a.imports.CommitHook = options.ImportCommitHook
		a.imports.Now = a.now
		a.imports.TypeSafe = a.integrations
	}
	a.wakeMonthly = options.WakeMonthly
	if a.wakeMonthly == nil {
		a.wakeMonthly = func() {}
	}
	a.wakeInvesting = options.WakeInvesting
	if a.wakeInvesting == nil {
		a.wakeInvesting = func() {}
	}
	a.tenants = options.Tenants
	if a.tenants == nil {
		var err error
		a.tenants, err = tenants.New(context.Background(), store, a.buildTenant)
		if err != nil {
			return nil, err
		}
		if err := a.tenants.Open(context.Background()); err != nil {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		created, err := store.Queries.GetRuntime(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Database is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, Status{Status: "ok", Database: "encrypted", CipherVersion: store.CipherVersion, CreatedAt: created})
	})
	mux.HandleFunc("GET /api/session", a.getSession)
	mux.HandleFunc("POST /api/session", a.signIn)
	mux.HandleFunc("DELETE /api/session", a.signOut)
	mux.HandleFunc("POST /api/setup", a.setup)
	mux.HandleFunc("POST /api/password", a.signedIn((*api).changePassword))
	mux.HandleFunc("GET /api/sessions", a.protected((*api).listSessions))
	mux.HandleFunc("GET /api/users", a.ownerOnly((*api).listUsers))
	mux.HandleFunc("POST /api/users", a.ownerOnly((*api).createUser))
	mux.HandleFunc("PUT /api/users/{id}/password", a.ownerOnly((*api).resetPassword))
	mux.HandleFunc("DELETE /api/users/{id}", a.ownerOnly((*api).deleteUser))
	// Every other endpoint exposes or changes private data and requires a
	// session whose temporary password was replaced.
	mux.HandleFunc("GET /api/settings", a.protected((*api).getSettings))
	mux.HandleFunc("PATCH /api/settings", a.protected((*api).updateSettings))
	mux.HandleFunc("GET /api/settings/tax", a.protected((*api).getTaxSettings))
	mux.HandleFunc("PUT /api/settings/tax", a.protected((*api).updateTaxSettings))
	mux.HandleFunc("GET /api/integrations", a.protected((*api).listIntegrations))
	mux.HandleFunc("PUT /api/integrations/{provider}", a.protected((*api).saveIntegration))
	mux.HandleFunc("POST /api/integrations/{provider}/check", a.protected((*api).checkIntegration))
	mux.HandleFunc("DELETE /api/integrations/{provider}", a.protected((*api).removeIntegration))
	mux.HandleFunc("GET /api/currencies", a.protected((*api).listCurrencies))
	mux.HandleFunc("GET /api/rates", a.protected((*api).getRates))
	mux.HandleFunc("GET /api/accounts", a.protected((*api).listAccounts))
	mux.HandleFunc("POST /api/accounts", a.protected((*api).createAccount))
	mux.HandleFunc("GET /api/transactions", a.protected((*api).listTransactions))
	mux.HandleFunc("POST /api/transactions", a.protected((*api).createTransaction))
	mux.HandleFunc("POST /api/transactions/bulk", a.protected((*api).bulkTransactions))
	mux.HandleFunc("POST /api/transactions/categorize", a.protected((*api).categorizeTransactions))
	mux.HandleFunc("GET /api/transactions/{id}", a.protected((*api).getTransaction))
	mux.HandleFunc("PATCH /api/transactions/{id}", a.protected((*api).updateTransaction))
	mux.HandleFunc("DELETE /api/transactions/{id}", a.protected((*api).deleteTransaction))
	mux.HandleFunc("GET /api/statistics", a.protected((*api).statistics))
	mux.HandleFunc("GET /api/dashboard", a.protected((*api).dashboard))
	mux.HandleFunc("GET /api/subscriptions", a.protected((*api).listSubscriptions))
	mux.HandleFunc("GET /api/categories", a.protected((*api).listCategories))
	mux.HandleFunc("POST /api/categories", a.protected(createEntity(catalog.Categories)))
	mux.HandleFunc("PATCH /api/categories/{id}", a.protected(renameEntity(catalog.Categories)))
	mux.HandleFunc("POST /api/categories/{id}/merge", a.protected(mergeEntity(catalog.Categories)))
	mux.HandleFunc("GET /api/payees", a.protected((*api).listPayees))
	mux.HandleFunc("POST /api/payees", a.protected(createEntity(catalog.Payees)))
	mux.HandleFunc("PATCH /api/payees/{id}", a.protected(renameEntity(catalog.Payees)))
	mux.HandleFunc("POST /api/payees/{id}/merge", a.protected(mergeEntity(catalog.Payees)))
	mux.HandleFunc("GET /api/rules", a.protected((*api).listRules))
	mux.HandleFunc("POST /api/rules", a.protected((*api).saveRule))
	mux.HandleFunc("PATCH /api/rules/{id}", a.protected((*api).updateRule))
	mux.HandleFunc("DELETE /api/rules/{id}", a.protected((*api).deleteRule))
	mux.HandleFunc("GET /api/imports", a.protected((*api).listImports))
	mux.HandleFunc("POST /api/imports", a.protected((*api).uploadImport))
	mux.HandleFunc("GET /api/imports/{id}", a.protected((*api).getImport))
	mux.HandleFunc("DELETE /api/imports/{id}", a.protected((*api).deleteImport))
	mux.HandleFunc("GET /api/imports/{id}/rows", a.protected((*api).listImportRows))
	mux.HandleFunc("PATCH /api/imports/{id}/rows/{row}", a.protected((*api).correctImportRow))
	mux.HandleFunc("PUT /api/imports/{id}/mapping", a.protected((*api).setImportMapping))
	mux.HandleFunc("POST /api/imports/{id}/suggest-mapping", a.protected((*api).suggestImportMapping))
	mux.HandleFunc("POST /api/imports/{id}/validate", a.protected((*api).validateImport))
	mux.HandleFunc("POST /api/imports/{id}/commit", a.protected((*api).commitImport))
	mux.HandleFunc("POST /api/imports/{id}/confirm", a.protected((*api).confirmImport))
	mux.HandleFunc("GET /api/scheduled-imports", a.protected((*api).listScheduledImports))
	mux.HandleFunc("POST /api/scheduled-imports/{period}/run", a.protected((*api).rerunScheduledImport))
	mux.HandleFunc("GET /api/notifications", a.protected((*api).listNotifications))
	mux.HandleFunc("POST /api/notifications/{id}/dismiss", a.protected((*api).dismissNotification))
	mux.HandleFunc("POST /api/notifications/{id}/resend", a.protected((*api).resendNotification))
	mux.HandleFunc("GET /api/investing", a.protected((*api).getInvesting))
	mux.HandleFunc("GET /api/investing/income", a.protected((*api).getIncome))
	mux.HandleFunc("GET /api/investing/income.csv", a.protected((*api).exportIncome))
	mux.HandleFunc("PUT /api/investing/settings", a.protected((*api).updateInvestingSettings))
	mux.HandleFunc("POST /api/investing/groups", a.protected((*api).createInvestingGroup))
	mux.HandleFunc("PUT /api/investing/groups/{id}", a.protected((*api).updateInvestingGroup))
	mux.HandleFunc("DELETE /api/investing/groups/{id}", a.protected((*api).deleteInvestingGroup))
	mux.HandleFunc("GET /api/chats", a.protected((*api).listChats))
	mux.HandleFunc("POST /api/chats", a.protected((*api).createChat))
	mux.HandleFunc("GET /api/chats/{id}", a.protected((*api).getChat))
	mux.HandleFunc("PATCH /api/chats/{id}", a.protected((*api).renameChat))
	mux.HandleFunc("DELETE /api/chats/{id}", a.protected((*api).deleteChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", a.protected((*api).askInChat))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "API endpoint not found")
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "API endpoint not found")
	})
	mux.Handle("/", staticFiles(assets))
	// Browsers identify cross-site requests with Sec-Fetch-Site or Origin.
	// Unsafe methods from another origin are rejected before any handler runs.
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "Cross-origin request rejected")
	}))
	protected := protection.Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api") {
			w.Header().Set("Cache-Control", "no-store")
		}
		protected.ServeHTTP(w, r)
	}), nil
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, errorBody{Error: message})
}

func writeFieldErrors(w http.ResponseWriter, fields map[string]string) {
	writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: "Check the highlighted fields", Fields: fields})
}

func staticFiles(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(assets, name)
		if err == nil && info.IsDir() {
			name = path.Join(name, "index.html")
		}
		content, err := assets.Open(name)
		if err != nil {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodHead {
				return
			}
			if page, err := fs.ReadFile(assets, "404.html"); err == nil {
				w.Write(page)
			} else {
				w.Write([]byte("Page not found"))
			}
			return
		}
		defer content.Close()
		info, err = content.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		seeker, ok := content.(io.ReadSeeker)
		if !ok {
			http.Error(w, "Asset unavailable", http.StatusInternalServerError)
			return
		}
		if strings.HasPrefix(name, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, path.Base(name), info.ModTime(), seeker)
	})
}
