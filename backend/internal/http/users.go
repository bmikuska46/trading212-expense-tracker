package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/tenants"
)

type userEntry struct {
	ID                 int64  `json:"id"`
	Email              string `json:"email"`
	Owner              bool   `json:"owner"`
	MustChangePassword bool   `json:"mustChangePassword"`
	CreatedAt          string `json:"createdAt"`
}

func newUserEntry(user dbgen.User) userEntry {
	return userEntry{ID: user.ID, Email: user.Email, Owner: user.ID == tenants.OwnerID,
		MustChangePassword: user.MustChangePassword == 1, CreatedAt: user.CreatedAt}
}

// buildTenant makes tenants without background jobs, for a server started
// without a registry. The owner's uses the pipeline and wake functions from
// the options.
func (a *api) buildTenant(_ context.Context, user tenants.User, store *database.Store) (*tenants.Tenant, error) {
	if user.ID == tenants.OwnerID {
		return &tenants.Tenant{Store: store, Imports: a.imports, WakeMonthly: a.wakeMonthly, WakeInvesting: a.wakeInvesting}, nil
	}
	pipeline := imports.New(store)
	pipeline.Now = a.now
	pipeline.TypeSafe = a.integrations
	pipeline.Settings = a.main
	return &tenants.Tenant{Store: store, Imports: pipeline, WakeMonthly: func() {}, WakeInvesting: func() {}}, nil
}

// changePassword replaces the signed-in user's password, including the
// temporary one the owner set, and signs out their other sessions.
func (a *api) changePassword(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	user, err := a.main.Queries.GetUser(r.Context(), current.userID)
	if err != nil {
		internalError(w, "load user")
		return
	}
	matches, err := auth.VerifyPassword(r.Context(), user.PasswordHash, input.CurrentPassword)
	if err != nil {
		internalError(w, "verify password")
		return
	}
	fields := map[string]string{}
	if !matches {
		fields["currentPassword"] = "The current password is incorrect"
	}
	if problem := auth.PasswordProblem(input.NewPassword); problem != "" {
		fields["newPassword"] = problem
	} else if input.NewPassword == input.CurrentPassword {
		fields["newPassword"] = "Choose a password different from the current one"
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	hash, err := auth.HashPassword(r.Context(), input.NewPassword)
	if err != nil {
		internalError(w, "hash password")
		return
	}
	tx, err := a.main.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin password change")
		return
	}
	defer tx.Rollback()
	queries := a.main.Queries.WithTx(tx)
	if err := queries.SetUserPassword(r.Context(), dbgen.SetUserPasswordParams{PasswordHash: hash, MustChangePassword: 0, ID: user.ID}); err != nil {
		internalError(w, "save password")
		return
	}
	if err := queries.DeleteUserSessions(r.Context(), dbgen.DeleteUserSessionsParams{UserID: user.ID, Keep: current.tokenHash}); err != nil {
		internalError(w, "end other sessions")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit password change")
		return
	}
	current.mustChangePassword = false
	writeJSON(w, http.StatusOK, sessionView{Authenticated: true, User: current.view()})
}

func (a *api) listUsers(w http.ResponseWriter, r *http.Request, _ *session) {
	users, err := a.main.Queries.ListUsers(r.Context())
	if err != nil {
		internalError(w, "list users")
		return
	}
	entries := make([]userEntry, 0, len(users))
	for _, user := range users {
		entries = append(entries, newUserEntry(user))
	}
	writeJSON(w, http.StatusOK, map[string][]userEntry{"users": entries})
}

// createUser adds a user with a temporary password and an empty ledger whose
// dates follow the owner's timezone until the user picks their own.
func (a *api) createUser(w http.ResponseWriter, r *http.Request, current *session) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	fields := map[string]string{}
	email, ok := auth.NormalizeEmail(input.Email)
	if !ok {
		fields["email"] = "Enter a single email address, such as name@example.com"
	} else if _, err := a.main.Queries.GetUserByEmail(r.Context(), email); err == nil {
		fields["email"] = "A user with this email already exists"
	} else if !errors.Is(err, sql.ErrNoRows) {
		internalError(w, "load user")
		return
	}
	if problem := auth.PasswordProblem(input.Password); problem != "" {
		fields["password"] = problem
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
		internalError(w, "begin user creation")
		return
	}
	defer tx.Rollback()
	user, err := a.main.Queries.WithTx(tx).CreateUser(r.Context(), dbgen.CreateUserParams{Email: email, PasswordHash: hash})
	if err != nil {
		// A concurrent request may have added the same email.
		writeFieldErrors(w, map[string]string{"email": "A user with this email already exists"})
		return
	}
	// The ledger is a separate file, so it is created before the user row
	// commits and removed again when the commit fails.
	if err := a.tenants.Create(r.Context(), tenants.User{ID: user.ID, Email: user.Email}, current.timezone); err != nil {
		internalError(w, "create ledger")
		return
	}
	if err := tx.Commit(); err != nil {
		if err := a.tenants.Remove(user.ID); err != nil {
			slog.Error("cannot remove the ledger of a user that was not created", "user", user.ID)
		}
		internalError(w, "commit user creation")
		return
	}
	writeJSON(w, http.StatusCreated, newUserEntry(user))
}

// memberID reads the path id of a user other than the owner.
func (a *api) memberID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(w, r, "User")
	if !ok {
		return 0, false
	}
	if id == tenants.OwnerID {
		writeError(w, http.StatusConflict, "Change your own password in your settings")
		return 0, false
	}
	return id, true
}

// resetPassword gives a user a new temporary password and signs them out.
func (a *api) resetPassword(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := a.memberID(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if problem := auth.PasswordProblem(input.Password); problem != "" {
		writeFieldErrors(w, map[string]string{"password": problem})
		return
	}
	user, err := a.main.Queries.GetUser(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}
	if err != nil {
		internalError(w, "load user")
		return
	}
	hash, err := auth.HashPassword(r.Context(), input.Password)
	if err != nil {
		internalError(w, "hash password")
		return
	}
	tx, err := a.main.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, "begin password reset")
		return
	}
	defer tx.Rollback()
	queries := a.main.Queries.WithTx(tx)
	if err := queries.SetUserPassword(r.Context(), dbgen.SetUserPasswordParams{PasswordHash: hash, MustChangePassword: 1, ID: id}); err != nil {
		internalError(w, "save password")
		return
	}
	if err := queries.DeleteUserSessions(r.Context(), dbgen.DeleteUserSessionsParams{UserID: id, Keep: []byte{}}); err != nil {
		internalError(w, "end sessions")
		return
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit password reset")
		return
	}
	user.MustChangePassword = 1
	writeJSON(w, http.StatusOK, newUserEntry(user))
}

// deleteUser removes a user and deletes their ledger for good.
func (a *api) deleteUser(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := a.memberID(w, r)
	if !ok {
		return
	}
	// Removing the row first signs the user out and stops new sign-ins
	// before their ledger goes away.
	deleted, err := a.main.Queries.DeleteUser(r.Context(), id)
	if err != nil {
		internalError(w, "delete user")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}
	if err := a.tenants.Remove(id); err != nil {
		internalError(w, "remove ledger")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
