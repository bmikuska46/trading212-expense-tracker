package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/tenants"
)

const (
	secureCookieName = "__Host-budget_session"
	plainCookieName  = "budget_session"
	maxBodyBytes     = 64 << 10
)

type session struct {
	tokenHash          []byte
	userID             int64
	email              string
	timezone           string
	mustChangePassword bool
}

func (s *session) owner() bool { return s.userID == tenants.OwnerID }

func (s *session) view() *userView {
	return &userView{Email: s.email, Timezone: s.timezone, Owner: s.owner(), MustChangePassword: s.mustChangePassword}
}

type userView struct {
	Email    string `json:"email"`
	Timezone string `json:"timezone"`
	// Owner is true for the user who set up the app and manages the others.
	Owner              bool `json:"owner"`
	MustChangePassword bool `json:"mustChangePassword"`
}

type sessionView struct {
	TurnstileSiteKey string    `json:"turnstileSiteKey"`
	SetupRequired    bool      `json:"setupRequired"`
	Authenticated    bool      `json:"authenticated"`
	User             *userView `json:"user"`
}

// decodeJSON reads one bounded JSON object and rejects unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Send JSON with Content-Type application/json")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Request body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "Request body is not valid JSON for this endpoint")
		}
		return false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Request body must contain one JSON object")
		return false
	}
	return true
}

// internalError logs a fixed description and never the underlying error, which
// could include stored values from SQL constraint messages.
func internalError(w http.ResponseWriter, operation string) {
	slog.Error("request failed", "operation", operation)
	writeError(w, http.StatusInternalServerError, "The server could not complete the request")
}

func (a *api) secureRequest(r *http.Request) bool {
	switch a.cookieSecure {
	case "always":
		return true
	case "never":
		return false
	}
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func (a *api) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	secure := a.secureRequest(r)
	name := plainCookieName
	if secure {
		// The __Host- prefix requires Secure and Path=/ and forbids Domain.
		name = secureCookieName
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: "/", Expires: expires, MaxAge: int(expires.Sub(a.now()).Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func (a *api) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := a.secureRequest(r)
	for _, name := range []string{secureCookieName, plainCookieName} {
		if name == secureCookieName && !secure {
			continue
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	}
}

func sessionToken(r *http.Request) (string, []byte, bool) {
	for _, name := range []string{secureCookieName, plainCookieName} {
		if cookie, err := r.Cookie(name); err == nil {
			if hash, ok := auth.SessionTokenHash(cookie.Value); ok {
				return cookie.Value, hash, true
			}
		}
	}
	return "", nil, false
}

// currentSession validates the cookie and extends sessions past half their lifetime.
func (a *api) currentSession(w http.ResponseWriter, r *http.Request) (*session, error) {
	token, hash, ok := sessionToken(r)
	if !ok {
		return nil, nil
	}
	now := a.now()
	row, err := a.main.Queries.GetSession(r.Context(), dbgen.GetSessionParams{TokenHash: hash, Now: auth.FormatTime(now)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expires, err := time.Parse(auth.TimeLayout, row.ExpiresAt)
	if err == nil && expires.Sub(now) < auth.SessionLifetime-auth.SessionRenewAfter {
		expires = now.Add(auth.SessionLifetime)
		if err := a.main.Queries.ExtendSession(r.Context(), dbgen.ExtendSessionParams{ExpiresAt: auth.FormatTime(expires), TokenHash: hash}); err != nil {
			return nil, err
		}
		a.setSessionCookie(w, r, token, expires)
	}
	tenant, ok := a.tenants.Get(row.UserID)
	if !ok {
		return nil, errors.New("ledger is not open")
	}
	timezone, err := tenant.Store.Queries.GetOwnerTimezone(r.Context())
	if err != nil {
		return nil, err
	}
	return &session{tokenHash: hash, userID: row.UserID, email: row.Email, timezone: timezone,
		mustChangePassword: row.MustChangePassword == 1}, nil
}

// forUser returns the api that works on the user's ledger.
func (a *api) forUser(userID int64) (*api, bool) {
	tenant, ok := a.tenants.Get(userID)
	if !ok {
		return nil, false
	}
	scoped := *a
	scoped.store, scoped.imports = tenant.Store, tenant.Imports
	scoped.wakeMonthly, scoped.wakeInvesting = tenant.WakeMonthly, tenant.WakeInvesting
	return &scoped, true
}

// signedIn runs next on the signed-in user's ledger, even before they
// replace a temporary password.
func (a *api) signedIn(next func(*api, http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, err := a.currentSession(w, r)
		if err != nil {
			internalError(w, "load session")
			return
		}
		if current == nil {
			writeError(w, http.StatusUnauthorized, "Sign in required")
			return
		}
		scoped, ok := a.forUser(current.userID)
		if !ok {
			internalError(w, "load ledger")
			return
		}
		next(scoped, w, r, current)
	}
}

// protected is signedIn for users who chose their own password.
func (a *api) protected(next func(*api, http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return a.signedIn(func(a *api, w http.ResponseWriter, r *http.Request, current *session) {
		if current.mustChangePassword {
			writeError(w, http.StatusForbidden, "Choose a new password first")
			return
		}
		next(a, w, r, current)
	})
}

// ownerOnly is protected for the owner alone.
func (a *api) ownerOnly(next func(*api, http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return a.protected(func(a *api, w http.ResponseWriter, r *http.Request, current *session) {
		if !current.owner() {
			writeError(w, http.StatusForbidden, "Only the owner can do this")
			return
		}
		next(a, w, r, current)
	})
}

func (a *api) ownerExists(ctx context.Context) (bool, error) {
	count, err := a.main.Queries.OwnerExists(ctx)
	return count > 0, err
}

func (a *api) getSession(w http.ResponseWriter, r *http.Request) {
	exists, err := a.ownerExists(r.Context())
	if err != nil {
		internalError(w, "load owner")
		return
	}
	view := sessionView{SetupRequired: !exists, TurnstileSiteKey: a.turnstileSiteKey}
	if exists {
		current, err := a.currentSession(w, r)
		if err != nil {
			internalError(w, "load session")
			return
		}
		if current != nil {
			view.Authenticated = true
			view.User = current.view()
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// startSession creates a session for the user and returns its cookie value.
func (a *api) startSession(ctx context.Context, queries *dbgen.Queries, userID int64, from origin) (string, time.Time, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := a.now()
	expires := now.Add(auth.SessionLifetime)
	if err := queries.DeleteExpiredSessions(ctx, auth.FormatTime(now)); err != nil {
		return "", time.Time{}, err
	}
	err = queries.CreateSession(ctx, dbgen.CreateSessionParams{TokenHash: hash, UserID: userID, CreatedAt: auth.FormatTime(now),
		ExpiresAt: auth.FormatTime(expires), Ip: from.ip, Country: from.country, Region: from.region, City: from.city, UserAgent: from.userAgent})
	return token, expires, err
}

func (a *api) setup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// Answer quickly once setup is complete, without spending a password hash.
	if exists, err := a.ownerExists(r.Context()); err != nil {
		internalError(w, "load owner")
		return
	} else if exists {
		writeError(w, http.StatusConflict, "Setup is already complete. Sign in instead.")
		return
	}
	fields := map[string]string{}
	email, ok := auth.NormalizeEmail(input.Email)
	if !ok {
		fields["email"] = "Enter a single email address, such as owner@example.com"
	}
	if problem := auth.PasswordProblem(input.Password); problem != "" {
		fields["password"] = problem
	}
	if input.Timezone == "" {
		input.Timezone = auth.DefaultTimezone
	}
	if !auth.ValidTimezone(input.Timezone) {
		fields["timezone"] = "Choose a timezone from the list"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	hash, err := auth.HashPassword(r.Context(), input.Password)
	if err != nil {
		internalError(w, "hash password")
		return
	}
	tx, err := a.main.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin setup")
		return
	}
	defer tx.Rollback()
	queries := a.main.Queries.WithTx(tx)
	// The owner table allows only id 1, so concurrent setup requests commit once.
	err = queries.CreateOwner(r.Context(), dbgen.CreateOwnerParams{Email: email, Timezone: input.Timezone})
	if err == nil {
		err = queries.CreateOwnerUser(r.Context(), dbgen.CreateOwnerUserParams{Email: email, PasswordHash: hash})
	}
	if err != nil {
		tx.Rollback()
		if exists, checkErr := a.ownerExists(r.Context()); checkErr == nil && exists {
			writeError(w, http.StatusConflict, "Setup is already complete. Sign in instead.")
			return
		}
		internalError(w, "create owner")
		return
	}
	token, expires, err := a.startSession(r.Context(), queries, tenants.OwnerID, a.signInOrigin(r))
	if err != nil {
		internalError(w, "create session")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit setup")
		return
	}
	a.setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusCreated, sessionView{TurnstileSiteKey: a.turnstileSiteKey, Authenticated: true,
		User: &userView{Email: email, Timezone: input.Timezone, Owner: true}})
}

func loginRateLimited(w http.ResponseWriter, wait time.Duration) {
	seconds := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
	w.Header().Set("Retry-After", seconds)
	writeError(w, http.StatusTooManyRequests, "Too many sign-in attempts. Try again in "+seconds+" seconds.")
}

func (a *api) signIn(w http.ResponseWriter, r *http.Request) {
	if wait := a.loginLimiter.Allow(a.now(), a.clientIP(r)); wait > 0 {
		loginRateLimited(w, wait)
		return
	}
	var input struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if a.turnstile != nil {
		verified, err := a.turnstile.Verify(r.Context(), input.TurnstileToken)
		if err != nil {
			slog.Warn("sign-in verification unavailable")
			writeError(w, http.StatusServiceUnavailable, "Security verification is unavailable. Try again.")
			return
		}
		if !verified {
			writeError(w, http.StatusForbidden, "Complete the security verification and try again.")
			return
		}
	}
	if wait := a.throttle.Begin(a.now(), input.Email); wait > 0 {
		loginRateLimited(w, wait)
		return
	}
	result := auth.LoginUnchecked
	defer func() { a.throttle.End(a.now(), input.Email, result) }()
	if exists, err := a.ownerExists(r.Context()); err != nil {
		internalError(w, "load owner")
		return
	} else if !exists {
		auth.SpendVerification(r.Context(), input.Password)
		writeError(w, http.StatusConflict, "Complete setup first")
		return
	}
	// The email column compares without case.
	user, err := a.main.Queries.GetUserByEmail(r.Context(), strings.TrimSpace(input.Email))
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internalError(w, "load user")
		return
	}
	// Always verify, so a wrong email takes as long as a wrong password.
	passwordMatches := false
	if found {
		passwordMatches, err = auth.VerifyPassword(r.Context(), user.PasswordHash, input.Password)
		if err != nil {
			internalError(w, "verify password")
			return
		}
	} else {
		auth.SpendVerification(r.Context(), input.Password)
	}
	if !passwordMatches {
		result = auth.LoginFailed
		writeError(w, http.StatusUnauthorized, "Email or password is incorrect")
		return
	}
	result = auth.LoginSucceeded
	token, expires, err := a.startSession(r.Context(), a.main.Queries, user.ID, a.signInOrigin(r))
	if err != nil {
		internalError(w, "create session")
		return
	}
	a.setSessionCookie(w, r, token, expires)
	tenant, ok := a.tenants.Get(user.ID)
	if !ok {
		internalError(w, "load ledger")
		return
	}
	timezone, err := tenant.Store.Queries.GetOwnerTimezone(r.Context())
	if err != nil {
		internalError(w, "load timezone")
		return
	}
	writeJSON(w, http.StatusOK, sessionView{TurnstileSiteKey: a.turnstileSiteKey, Authenticated: true, User: &userView{Email: user.Email, Timezone: timezone,
		Owner: user.ID == tenants.OwnerID, MustChangePassword: user.MustChangePassword == 1}})
}

func (a *api) signOut(w http.ResponseWriter, r *http.Request) {
	if _, hash, ok := sessionToken(r); ok {
		if err := a.main.Queries.DeleteSession(r.Context(), hash); err != nil {
			internalError(w, "delete session")
			return
		}
	}
	a.clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}
