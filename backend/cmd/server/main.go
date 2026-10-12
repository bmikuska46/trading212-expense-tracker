package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/config"
	"budget-tracking/backend/internal/database"
	httpserver "budget-tracking/backend/internal/http"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/tenants"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "import-trading-go" {
		if err := importTradingGoFile(os.Args[2]); err != nil {
			slog.Error("trading-go import failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.TurnstileSecretKey == "" {
		return errors.New("APP_TURNSTILE_SECRET_KEY is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := database.Open(ctx, cfg.DatabasePath, cfg.KeyFile)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	client := integrations.NewClient()
	users, err := tenants.New(ctx, store, func(ctx context.Context, user tenants.User, ledger *database.Store) (*tenants.Tenant, error) {
		return tenant(ctx, client, store, user, ledger)
	})
	if err != nil {
		return err
	}
	if err := users.Open(ctx); err != nil {
		return err
	}
	defer users.Close()
	handler, err := httpserver.New(store, cfg.WebDirectory, httpserver.Options{CookieSecure: cfg.CookieSecure,
		Integrations: client, Tenants: users, Turnstile: auth.NewTurnstile(cfg.TurnstileSecretKey), TrustedProxies: cfg.TrustedProxies})
	if err != nil {
		return errors.New("static frontend is missing; build web first")
	}
	server := &http.Server{Addr: cfg.ListenAddress, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	// Every user's jobs stop before the databases close.
	users.Start(ctx)
	defer func() {
		stop()
		users.Wait()
	}()
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	slog.Info("server started", "address", cfg.ListenAddress, "sqlcipher", store.CipherVersion)
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// tenant wires the jobs of one user's ledger. The rate job fetches once now
// and then daily at 18:00 Europe/Bratislava. The monthly import job requests
// Trading 212 reports at 06:00 on the 1st, the notification job emails import
// events, the investing job snapshots the Trading 212 portfolio hourly, and
// the income job imports dividends, interest, and cashback with the NBS rates
// to convert them. TypeSafe and Resend settings come from the main database.
func tenant(ctx context.Context, client *integrations.Client, main *database.Store, user tenants.User, store *database.Store) (*tenants.Tenant, error) {
	// Uploads interrupted by a stop can never finish; fail them cleanly.
	if recovered, err := imports.Recover(ctx, store); err != nil {
		return nil, err
	} else if recovered > 0 {
		slog.Warn("marked interrupted imports as failed", "user", user.ID, "count", recovered)
	}
	pipeline := imports.New(store)
	pipeline.TypeSafe = client
	pipeline.Settings = main
	monthly := jobs.NewMonthly(store, client, pipeline)
	investing := jobs.NewInvesting(store, client)
	income := jobs.NewIncome(store, client)
	notifications := jobs.NewNotifications(store, client)
	notifications.Settings = main
	if user.ID != tenants.OwnerID {
		notifications.Recipient = user.Email
	}
	workers := []func(context.Context){jobs.NewRates(store).Run, monthly.Run, notifications.Run, investing.Run, income.Run}
	return &tenants.Tenant{Store: store, Imports: pipeline, WakeMonthly: monthly.Wake,
		WakeInvesting: func() { investing.Wake(); income.Wake() },
		Run: func(ctx context.Context) {
			var group sync.WaitGroup
			for _, work := range workers {
				group.Go(func() { work(ctx) })
			}
			group.Wait()
		}}, nil
}

func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	address := net.JoinHostPort(host, port)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + address + "/api/health")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("healthcheck failed")
	}
	return nil
}
