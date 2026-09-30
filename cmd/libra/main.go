// Command libra is a single binary that serves the web app, the management
// API, the runtime API and the data pipeline, backed by Postgres. It also has
// operator subcommands (sign-in links, API keys, demo data, pipeline runs).
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/reynerpantou/libra/internal/auth"
	"github.com/reynerpantou/libra/internal/config"
	"github.com/reynerpantou/libra/internal/database"
	"github.com/reynerpantou/libra/internal/handlers"
	"github.com/reynerpantou/libra/internal/middleware"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/simulate"
)

func main() {
	cfg := config.Load()
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seedOwner(db, cfg); err != nil {
		log.Fatalf("seed owner: %v", err)
	}
	if len(os.Args) > 1 {
		if err := runCommand(db, cfg, os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	serve(db, cfg)
}

func serve(db *sql.DB, cfg config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store := serving.NewStore(db)
	if err := store.Reload(ctx, true); err != nil {
		log.Fatalf("load config snapshot: %v", err)
	}
	go store.Watch(ctx, 2*time.Second)

	logCtx, stopLogger := context.WithCancel(context.Background())
	logger := serving.NewLogger(db)
	go logger.Run(logCtx)

	sched := pipeline.NewScheduler(db)
	go sched.Loop(ctx, cfg.PipelineInterval)
	go purgeSessions(ctx, db)

	s := handlers.New(db, cfg, store, logger, sched)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           routes(s, db, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		log.Printf("libra listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()
	<-ctx.Done()
	log.Println("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	// Flush buffered exposures after requests have drained.
	stopLogger()
	logger.Wait()
}

func routes(s *handlers.Server, db *sql.DB, cfg config.Config) http.Handler {
	api := http.NewServeMux()
	loginRL := middleware.NewRateLimit(20, time.Minute, s.ClientIP)
	signedIn := func(role string, h http.HandlerFunc) http.Handler {
		return middleware.Chain(h, middleware.RequireAuth(db), middleware.CSRF, middleware.RequireRole(role))
	}
	viewer := func(h http.HandlerFunc) http.Handler { return signedIn("viewer", h) }
	editor := func(h http.HandlerFunc) http.Handler { return signedIn("editor", h) }
	admin := func(h http.HandlerFunc) http.Handler { return signedIn("admin", h) }

	// Sign-in
	limited := http.RedirectHandler("/login?error=rate_limited", http.StatusSeeOther)
	api.Handle("GET /auth/providers", http.HandlerFunc(s.AuthProviders))
	api.Handle("GET /auth/{provider}/start", loginRL.WrapWith(http.HandlerFunc(s.AuthStart), limited))
	api.Handle("GET /auth/{provider}/callback", loginRL.WrapWith(http.HandlerFunc(s.AuthCallback), limited))
	api.Handle("POST /auth/{provider}/callback", loginRL.WrapWith(http.HandlerFunc(s.AuthCallback), limited))
	api.Handle("POST /auth/link", loginRL.Wrap(http.HandlerFunc(s.AuthLink)))
	api.Handle("POST /logout", viewer(s.Logout))
	api.Handle("GET /me", viewer(s.Me))

	// People and keys
	api.Handle("GET /users", viewer(s.ListUsers))
	api.Handle("POST /users", admin(s.CreateUser))
	api.Handle("PUT /users/{id}", admin(s.UpdateUser))
	api.Handle("DELETE /users/{id}", admin(s.DeleteUser))
	api.Handle("GET /api-keys", admin(s.ListAPIKeys))
	api.Handle("POST /api-keys", admin(s.CreateAPIKey))
	api.Handle("DELETE /api-keys/{id}", admin(s.RevokeAPIKey))

	// Businesses and metric definitions
	api.Handle("GET /businesses", viewer(s.ListBusinesses))
	api.Handle("POST /businesses", admin(s.CreateBusiness))
	api.Handle("GET /businesses/{id}", viewer(s.GetBusiness))
	api.Handle("PUT /businesses/{id}", admin(s.UpdateBusiness))
	api.Handle("GET /businesses/{id}/measures", viewer(s.ListMeasures))
	api.Handle("POST /businesses/{id}/measures", editor(s.CreateMeasure))
	api.Handle("PUT /measures/{id}", editor(s.UpdateMeasure))
	api.Handle("DELETE /measures/{id}", editor(s.DeleteMeasure))
	api.Handle("GET /businesses/{id}/metrics", viewer(s.ListMetrics))
	api.Handle("POST /businesses/{id}/metrics", editor(s.CreateMetric))
	api.Handle("PUT /metrics/{id}", editor(s.UpdateMetric))
	api.Handle("DELETE /metrics/{id}", editor(s.DeleteMetric))
	api.Handle("POST /businesses/{id}/formula/validate", viewer(s.ValidateFormula))
	api.Handle("POST /businesses/{id}/formula/preview", viewer(s.PreviewFormula))
	api.Handle("GET /businesses/{id}/metric-groups", viewer(s.ListMetricGroups))
	api.Handle("POST /businesses/{id}/metric-groups", editor(s.CreateMetricGroup))
	api.Handle("PUT /metric-groups/{id}", editor(s.UpdateMetricGroup))
	api.Handle("DELETE /metric-groups/{id}", editor(s.DeleteMetricGroup))
	api.Handle("GET /businesses/{id}/events/summary", viewer(s.EventSummary))
	api.Handle("GET /businesses/{id}/events/recent", viewer(s.RecentEvents))

	// Layers and experiments
	api.Handle("GET /layers", viewer(s.ListLayers))
	api.Handle("POST /layers", admin(s.CreateLayer))
	api.Handle("PUT /layers/{id}", admin(s.UpdateLayer))
	api.Handle("GET /experiments", viewer(s.ListExperiments))
	api.Handle("POST /experiments", editor(s.CreateExperiment))
	api.Handle("GET /experiments/{id}", viewer(s.GetExperiment))
	api.Handle("PUT /experiments/{id}", editor(s.UpdateExperiment))
	api.Handle("POST /experiments/{id}/actions/{action}", editor(s.ExperimentAction))
	api.Handle("PUT /experiments/{id}/traffic", editor(s.SetTraffic))
	api.Handle("POST /experiments/{id}/whitelist", editor(s.AddWhitelist))
	api.Handle("DELETE /experiments/{id}/whitelist/{unit}", editor(s.RemoveWhitelist))
	api.Handle("GET /experiments/{id}/history", viewer(s.ExperimentHistory))
	api.Handle("POST /experiments/{id}/clone", editor(s.CloneExperiment))
	api.Handle("GET /experiments/{id}/report", viewer(s.ExperimentReport))
	api.Handle("GET /experiments/{id}/trend", viewer(s.ExperimentTrend))
	api.Handle("GET /experiments/{id}/exposures", viewer(s.ExposureDaily))

	// Tools and pipeline
	api.Handle("POST /tools/diagnose", viewer(s.Diagnose))
	api.Handle("GET /tools/params", viewer(s.ParamSearch))
	api.Handle("GET /pipeline", viewer(s.PipelineStatus))
	api.Handle("POST /pipeline/run", editor(s.RunPipeline))

	// Runtime API for services (API keys, no cookies)
	keys := middleware.NewAPIKeys(db)
	api.Handle("POST /v1/resolve", keys.Require("runtime")(http.HandlerFunc(s.Resolve)))
	api.Handle("POST /v1/exposures", keys.Require("runtime")(http.HandlerFunc(s.IngestExposures)))
	api.Handle("POST /v1/events", keys.Require("ingest")(http.HandlerFunc(s.IngestEvents)))

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", middleware.NoStore(api)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok (config v%d)\n", s.Store.Snapshot().Version)
	})
	mux.Handle("/", s.SPAHandler())
	return middleware.Chain(mux, middleware.Recover, middleware.Logger, middleware.SecurityHeaders(cfg.CookieSecure))
}

// seedOwner creates the owner account on an empty database.
func seedOwner(db *sql.DB, cfg config.Config) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	var email any
	if cfg.AdminEmail != "" {
		email = cfg.AdminEmail
	}
	if n > 0 {
		if email != nil {
			_, err := db.Exec(`UPDATE users SET email = $1 WHERE is_owner AND email IS NULL`, email)
			return err
		}
		return nil
	}
	if _, err := db.Exec(`INSERT INTO users (username, email, role, is_owner) VALUES ($1, $2, 'admin', true)`, cfg.AdminUser, email); err != nil {
		return err
	}
	if email == nil {
		log.Printf("created owner %q — run `libra sign-in-link %s` to sign in, or set LIBRA_ADMIN_EMAIL", cfg.AdminUser, cfg.AdminUser)
	}
	return nil
}

func purgeSessions(ctx context.Context, db *sql.DB) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_ = auth.PurgeExpiredSessions(db)
		for _, q := range []string{`DELETE FROM auth_flows WHERE expires_at < now()`, `DELETE FROM sign_in_links WHERE expires_at < now()`} {
			if _, err := db.Exec(q); err != nil {
				log.Printf("purge sign-in state: %v", err)
			}
		}
	}
}

const usage = `usage:
  libra                                        run the server
  libra sign-in-link <username>                print a one-time sign-in link (valid 15 minutes)
  libra api-key <name> <scope>[,<scope>]       create an API key (scopes: runtime, ingest)
  libra pipeline                               run the data pipeline once
  libra demo [-users N] [-days N]              seed the demo "search" business, simulate traffic, run the pipeline
  libra simulate [-business K] [-users N] [-days N] [-seed S]
                                               generate more synthetic traffic for a business`

func runCommand(db *sql.DB, cfg config.Config, cmd string, args []string) error {
	ctx := context.Background()
	switch cmd {
	case "sign-in-link":
		if len(args) != 1 {
			return errors.New(usage)
		}
		var id int64
		if err := db.QueryRow(`SELECT id FROM users WHERE lower(username) = lower($1)`, args[0]).Scan(&id); err != nil {
			return fmt.Errorf("no user %q", args[0])
		}
		link, err := handlers.NewSignInLink(db, cfg.PublicURL, id)
		if err != nil {
			return err
		}
		fmt.Printf("One-time sign-in link for %q (valid 15 minutes):\n\n  %s\n\n", args[0], link)
	case "api-key":
		if len(args) != 2 {
			return errors.New(usage)
		}
		scopes := strings.Split(args[1], ",")
		for _, sc := range scopes {
			if sc != "runtime" && sc != "ingest" {
				return fmt.Errorf("unknown scope %q", sc)
			}
		}
		k, err := handlers.NewAPIKey(ctx, db, args[0], scopes, 0)
		if err != nil {
			return err
		}
		fmt.Printf("API key %q (%s) — shown once, store it now:\n\n  %s\n\n", k.Name, args[1], k.Key)
	case "pipeline":
		st, err := pipeline.RunSettled(ctx, db, "cli", 0)
		if err != nil {
			return err
		}
		fmt.Printf("pipeline: %d exposures, %d events, %d measures backfilled, %d rows written in %.1fs\n",
			st.Exposures, st.Events, st.MeasuresBackfill, st.Rows, st.Seconds)
	case "demo", "simulate":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		users := fs.Int("users", 20000, "simulated users")
		days := fs.Int("days", 14, "days of history (max 30)")
		business := fs.String("business", "search", "business key")
		seed := fs.Int64("seed", time.Now().UnixNano(), "random seed")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if cmd == "demo" {
			var owner int64
			_ = db.QueryRow(`SELECT id FROM users WHERE is_owner`).Scan(&owner)
			if err := simulate.Seed(ctx, db, owner); err != nil && !errors.Is(err, simulate.ErrSeeded) {
				return err
			} else if err == nil {
				fmt.Println(`seeded business "search": 8 measures, 10 metrics, 3 metric groups, 2 layers, 3 experiments`)
			} else {
				fmt.Println(`business "search" already exists; adding traffic`)
			}
		}
		fmt.Printf("simulating %d users over %d days...\n", *users, *days)
		st, err := simulate.Generate(ctx, db, simulate.Options{Business: *business, Users: *users, Days: *days, Seed: *seed})
		if err != nil {
			return err
		}
		fmt.Printf("wrote %d exposures and %d events\n", st.Exposures, st.Events)
		ps, err := pipeline.RunSettled(ctx, db, cmd, 0)
		if err != nil {
			return err
		}
		fmt.Printf("pipeline: %d assignments, %d measure rows in %.1fs\n", ps.Assignments, ps.Rows, ps.Seconds)
	default:
		return errors.New(usage)
	}
	return nil
}
