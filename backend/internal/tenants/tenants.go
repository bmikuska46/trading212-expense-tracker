// Package tenants keeps every user's ledger open. The owner's ledger is the
// main database, which also holds the sign-ins of all users and the TypeSafe
// and Resend settings they share. Every other user has a database file of
// their own, encrypted with the same key, so no query can mix two users' data.
package tenants

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"budget-tracking/backend/internal/database"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
)

// OwnerID is the user who set up the app. Their ledger is the main database.
const OwnerID = 1

type User struct {
	ID    int64
	Email string
}

// Tenant is one user's ledger and the work that runs on it.
type Tenant struct {
	Store   *database.Store
	Imports *imports.Pipeline
	// WakeMonthly asks the monthly import job to look at a rerun right away.
	WakeMonthly func()
	// WakeInvesting asks the snapshot and income jobs to look at a changed
	// Trading 212 key.
	WakeInvesting func()
	// Run does the background work until the context ends. Nil has none.
	Run func(context.Context)
}

// Build makes the tenant of a user's opened ledger. It runs before the
// tenant serves requests, so it may also recover interrupted work.
type Build func(ctx context.Context, user User, store *database.Store) (*Tenant, error)

type entry struct {
	tenant *Tenant
	cancel context.CancelFunc
	done   chan struct{}
}

type Registry struct {
	main  *database.Store
	build Build

	mu      sync.Mutex
	entries map[int64]*entry
	// ctx is set by Start. Tenants added before it start with it.
	ctx context.Context
}

// New registers the owner's tenant on the main database.
func New(ctx context.Context, main *database.Store, build Build) (*Registry, error) {
	r := &Registry{main: main, build: build, entries: map[int64]*entry{}}
	owner, err := build(ctx, User{ID: OwnerID}, main)
	if err != nil {
		return nil, err
	}
	r.entries[OwnerID] = &entry{tenant: owner}
	return r, nil
}

// Path is where a user other than the owner keeps their ledger.
func (r *Registry) Path(userID int64) string {
	return filepath.Join(filepath.Dir(r.main.Path), "users", strconv.FormatInt(userID, 10)+".db")
}

// Open opens the ledgers of all users other than the owner.
func (r *Registry) Open(ctx context.Context) error {
	users, err := r.main.Queries.ListUsers(ctx)
	if err != nil {
		return errors.New("cannot list users")
	}
	for _, user := range users {
		if user.ID == OwnerID {
			continue
		}
		store, err := database.Open(ctx, r.Path(user.ID), r.main.KeyFile)
		if err != nil {
			return err
		}
		if err := r.add(ctx, User{ID: user.ID, Email: user.Email}, store); err != nil {
			store.DB.Close()
			return err
		}
	}
	return nil
}

func (r *Registry) add(ctx context.Context, user User, store *database.Store) error {
	tenant, err := r.build(ctx, user, store)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	added := &entry{tenant: tenant}
	r.entries[user.ID] = added
	if r.ctx != nil {
		r.start(added)
	}
	return nil
}

// start runs a tenant's work; the caller holds mu.
func (r *Registry) start(e *entry) {
	if e.tenant.Run == nil || e.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	e.cancel, e.done = cancel, make(chan struct{})
	go func() {
		defer close(e.done)
		e.tenant.Run(ctx)
	}()
}

// Start runs the work of every tenant, and of tenants created later, until
// the context ends. Wait returns once all of it has stopped.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = ctx
	for _, e := range r.entries {
		r.start(e)
	}
}

func (r *Registry) Wait() {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	for _, e := range entries {
		if e.done != nil {
			<-e.done
		}
	}
}

func (r *Registry) Get(userID int64) (*Tenant, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[userID]
	if !ok {
		return nil, false
	}
	return e.tenant, true
}

// Create makes the empty ledger of a new user, whose dates follow timezone.
func (r *Registry) Create(ctx context.Context, user User, timezone string) error {
	path := r.Path(user.ID)
	// A file left behind must never become a new user's ledger.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("a ledger file already exists for this user")
	}
	store, err := database.Open(ctx, path, r.main.KeyFile)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		store.DB.Close()
		database.Remove(path)
		return err
	}
	if err := store.Queries.CreateOwner(ctx, dbgen.CreateOwnerParams{Email: user.Email, Timezone: timezone}); err != nil {
		return fail(errors.New("cannot create the user's ledger"))
	}
	if err := r.add(ctx, user, store); err != nil {
		return fail(err)
	}
	return nil
}

// Remove stops the work of a user other than the owner and deletes their ledger.
func (r *Registry) Remove(userID int64) error {
	if userID == OwnerID {
		return errors.New("the owner's ledger cannot be removed")
	}
	r.mu.Lock()
	e, ok := r.entries[userID]
	delete(r.entries, userID)
	r.mu.Unlock()
	if ok {
		if e.cancel != nil {
			e.cancel()
			<-e.done
		}
		e.tenant.Store.DB.Close()
	}
	return database.Remove(r.Path(userID))
}

// Close closes the ledgers of all users other than the owner, after Wait.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.entries {
		if id != OwnerID {
			e.tenant.Store.DB.Close()
		}
	}
}
