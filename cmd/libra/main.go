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
	"slices"
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
	if len(os.Args) == 1 {
		reportSignIn(cfg)
	}
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if len(os.Args) > 1 {
		if err := runCommand(db, cfg, os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	announceSetup(db, cfg)
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
	go s.RolloutLoop(ctx, 30*time.Second)
	go s.TuningLoop(ctx, time.Minute)
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
	limited := http.RedirectHandler(config.BasePath+"/login?error=rate_limited", http.StatusSeeOther)
	api.Handle("GET /auth/providers", http.HandlerFunc(s.AuthProviders))
	api.Handle("GET /auth/{provider}/start", loginRL.WrapWith(http.HandlerFunc(s.AuthStart), limited))
	api.Handle("GET /auth/{provider}/callback", loginRL.WrapWith(http.HandlerFunc(s.AuthCallback), limited))
	api.Handle("POST /auth/{provider}/callback", loginRL.WrapWith(http.HandlerFunc(s.AuthCallback), limited))
	api.Handle("POST /auth/link", loginRL.Wrap(http.HandlerFunc(s.AuthLink)))
	api.Handle("POST /setup/start", loginRL.Wrap(http.HandlerFunc(s.SetupStart)))
	api.Handle("POST /logout", viewer(s.Logout))
	api.Handle("GET /me", viewer(s.Me))

	// People and keys
	api.Handle("GET /users", viewer(s.ListUsers))
	api.Handle("GET /users/search", editor(s.SearchUsers))
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
	api.Handle("DELETE /businesses/{id}", admin(s.DeleteBusiness))
	api.Handle("GET /businesses/{id}/delete-check", admin(s.DeleteCheck))
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
	api.Handle("GET /platforms", viewer(s.ListPlatforms))
	api.Handle("POST /platforms", admin(s.CreatePlatform))
	api.Handle("GET /platforms/{pid}", viewer(s.GetPlatform))
	api.Handle("PUT /platforms/{pid}", admin(s.UpdatePlatform))
	api.Handle("DELETE /platforms/{pid}", admin(s.DeletePlatform))
	api.Handle("GET /platforms/{pid}/measures", viewer(s.ListMeasures))
	api.Handle("POST /platforms/{pid}/measures", editor(s.CreateMeasure))
	api.Handle("GET /platforms/{pid}/metrics", viewer(s.ListMetrics))
	api.Handle("POST /platforms/{pid}/metrics", editor(s.CreateMetric))
	api.Handle("POST /platforms/{pid}/formula/validate", viewer(s.ValidateFormula))
	api.Handle("POST /platforms/{pid}/formula/preview", viewer(s.PreviewFormula))
	api.Handle("GET /platforms/{pid}/metric-groups", viewer(s.ListMetricGroups))
	api.Handle("POST /platforms/{pid}/metric-groups", editor(s.CreateMetricGroup))
	api.Handle("GET /metric-groups", viewer(s.AllMetricGroups))
	api.Handle("GET /metrics", viewer(s.AllMetrics))
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
	api.Handle("GET /experiments/{id}/rollouts", viewer(s.ListRollouts))
	api.Handle("DELETE /experiments/{id}/rollouts/{rid}", editor(s.CancelRollout))
	api.Handle("POST /experiments/{id}/whitelist", editor(s.AddWhitelist))
	api.Handle("DELETE /experiments/{id}/whitelist/{unit}", editor(s.RemoveWhitelist))
	api.Handle("POST /experiments/{id}/whitelist/remove", editor(s.RemoveWhitelistMany))
	api.Handle("GET /experiments/{id}/history", viewer(s.ExperimentHistory))
	api.Handle("POST /experiments/{id}/clone", editor(s.CloneExperiment))
	api.Handle("POST /experiments/{id}/reviewers", editor(s.InviteReviewers))
	api.Handle("GET /experiments/{id}/report", viewer(s.ExperimentReport))
	api.Handle("GET /experiments/{id}/trend", viewer(s.ExperimentTrend))
	api.Handle("GET /experiments/{id}/exposures", viewer(s.ExposureDaily))
	api.Handle("GET /experiments/{id}/params", viewer(s.ParamUsage))
	api.Handle("GET /tunings", viewer(s.ListTunings))
	api.Handle("POST /tunings", editor(s.CreateTuning))
	api.Handle("POST /tunings/preview", viewer(s.PreviewTuning))
	api.Handle("GET /tunings/{id}", viewer(s.GetTuning))
	api.Handle("PUT /tunings/{id}", editor(s.UpdateTuning))
	api.Handle("POST /tunings/{id}/advance", editor(s.AdvanceTuning))
	api.Handle("GET /tunings/{id}/surface", viewer(s.TuningSurface))
	api.Handle("GET /diversions", viewer(s.ListDiversions))
	api.Handle("POST /diversions", admin(s.CreateDiversion))
	api.Handle("PUT /diversions/{key}", admin(s.UpdateDiversion))
	api.Handle("DELETE /diversions/{key}", admin(s.DeleteDiversion))
	api.Handle("GET /attributes", viewer(s.ListAttributes))
	api.Handle("GET /attributes/discovered", viewer(s.DiscoveredAttributes))
	api.Handle("POST /attributes", editor(s.CreateAttribute))
	api.Handle("PUT /attributes/{id}", editor(s.UpdateAttribute))
	api.Handle("DELETE /attributes/{id}", editor(s.DeleteAttribute))

	// Tools and pipeline
	api.Handle("POST /tools/diagnose", viewer(s.Diagnose))
	api.Handle("GET /tools/params", viewer(s.ParamSearch))
	api.Handle("GET /parameters", viewer(s.ListParameters))
	api.Handle("GET /parameters/launched", viewer(s.LaunchedConfig))
	api.Handle("GET /pipeline", viewer(s.PipelineStatus))
	api.Handle("POST /pipeline/run", editor(s.RunPipeline))

	// Runtime API for services (API keys, no cookies)
	keys := middleware.NewAPIKeys(db)
	// Everything is served under /libra/api: services call
	// POST /libra/api/v1/abtest/experiments to get a unit's AB tests.
	abtest := keys.Require("runtime")(http.HandlerFunc(s.Resolve))
	exposures := keys.Require("runtime")(http.HandlerFunc(s.IngestExposures))
	api.Handle("POST /v1/abtest/experiments", abtest)
	api.Handle("POST /v1/abtest/exposures", exposures)
	api.Handle("POST /v1/events", keys.Require("ingest")(http.HandlerFunc(s.IngestEvents)))
	// Older names, kept so existing integrations keep working.
	api.Handle("POST /v1/resolve", abtest)
	api.Handle("POST /v1/exposures", exposures)

	mux := http.NewServeMux()
	base := config.BasePath
	mux.Handle(base+"/api/", http.StripPrefix(base+"/api", middleware.NoStore(api)))
	health := func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok (config v%d)\n", s.Store.Snapshot().Version)
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET "+base+"/healthz", health)
	mux.Handle(base+"/", http.StripPrefix(base, s.SPAHandler()))
	// Everything else (/, /libra without the slash, old bookmarks) goes to the app.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/", http.StatusFound)
	})
	return middleware.Chain(mux, middleware.Recover, middleware.Logger, middleware.SecurityHeaders(cfg.CookieSecure))
}

// reportSignIn says at startup which sign-in buttons will show, and what's
// missing when a provider is only partly configured.
func reportSignIn(cfg config.Config) {
	var on []string
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" {
		on = append(on, "Google")
	} else if cfg.GoogleClientID != "" || cfg.GoogleClientSecret != "" {
		log.Printf("sign-in: Google is OFF — set both LIBRA_GOOGLE_CLIENT_ID and LIBRA_GOOGLE_CLIENT_SECRET")
	}
	apple := []string{cfg.AppleClientID, cfg.AppleTeamID, cfg.AppleKeyID, cfg.ApplePrivateKey}
	switch n := len(slices.DeleteFunc(slices.Clone(apple), func(v string) bool { return v == "" })); n {
	case 4:
		on = append(on, "Apple")
	case 0:
	default:
		log.Printf("sign-in: Apple is OFF — set all of LIBRA_APPLE_CLIENT_ID, _TEAM_ID, _KEY_ID and _PRIVATE_KEY")
	}
	if len(on) == 0 {
		log.Printf("sign-in: no provider configured, so no sign-in buttons will show — add LIBRA_GOOGLE_CLIENT_ID and LIBRA_GOOGLE_CLIENT_SECRET to .env")
		return
	}
	log.Printf("sign-in: %s (callbacks go to %s/api/auth/…/callback)", strings.Join(on, " + "), cfg.AppURL())
}

// announceSetup prints a fresh owner setup link while nobody can sign in
// as the owner (a new install). There's nothing to configure: open the link,
// sign in with Google or Apple, and that account is the owner.
func announceSetup(db *sql.DB, cfg config.Config) {
	link, err := handlers.NewSetupLink(context.Background(), db, cfg.PublicURL)
	if err != nil {
		log.Printf("owner setup: %v", err)
		return
	}
	if link == "" {
		return
	}
	log.Printf(`
  ┌─ Libra has no owner yet ───────────────────────────────────────────
  │ Open this link and sign in with Google or Apple to become the owner:
  │
  │   %s
  │
  │ It works once and expires in 24 hours. A new one is printed on every
  │ start until someone claims it (or run: libra setup-link).
  └────────────────────────────────────────────────────────────────────`, link)
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
  libra setup-link                             print a new owner setup link (only while nobody can sign in as owner)
  libra list-users                             list accounts (username, email, role)
  libra sign-in-link <username>                print a one-time sign-in link (valid 15 minutes)
  libra api-key <name> <scope>[,<scope>]       create an API key (scopes: runtime, ingest)
  libra pipeline                               run the data pipeline once
  libra demo [-users N] [-days N]              build the full demo (every feature) on an empty database
  libra reset -yes                             drop every table and all data, then recreate an empty database
  libra demo-tuning [-algorithm A] [-rounds N]  add an AB Tuning study to the demo and play its first rounds
  libra simulate [-platform P] [-business K] [-users N] [-days N] [-seed S]
                                               generate more synthetic traffic for a business`

func runCommand(db *sql.DB, cfg config.Config, cmd string, args []string) error {
	ctx := context.Background()
	switch cmd {
	case "setup-link":
		link, err := handlers.NewSetupLink(ctx, db, cfg.PublicURL)
		if err != nil {
			return err
		}
		if link == "" {
			return errors.New("Libra already has an owner who can sign in; use sign-in-link for recovery")
		}
		fmt.Printf("Owner setup link (valid 24 hours, replaces any earlier one):\n\n  %s\n\n", link)
	case "list-users":
		rows, err := db.Query(`SELECT username, COALESCE(email, '—'), CASE WHEN is_owner THEN 'owner' ELSE role END
		                       FROM users ORDER BY is_owner DESC, role, lower(username)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u, e, role string
			if err := rows.Scan(&u, &e, &role); err != nil {
				return err
			}
			fmt.Printf("%-24s %-36s %s\n", u, e, role)
		}
		return rows.Err()
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
	case "demo":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		users := fs.Int("users", 20000, "simulated users on Demo Shop")
		days := fs.Int("days", 14, "days of history (max 30)")
		seed := fs.Int64("seed", time.Now().UnixNano()%1_000_000, "random seed")
		if err := fs.Parse(args); err != nil {
			return err
		}
		var owner int64
		_ = db.QueryRow(`SELECT id FROM users WHERE is_owner`).Scan(&owner)
		start := time.Now()
		fmt.Println("building the demo (takes a few minutes)...")
		err := simulate.FullDemo(ctx, db, owner, simulate.DemoOptions{Users: *users, Days: *days, Seed: *seed,
			Log: func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }})
		if errors.Is(err, simulate.ErrSeeded) || errors.Is(err, simulate.ErrDirty) {
			return fmt.Errorf("%w — the demo needs an empty database: run `make demo-fresh` (drops everything first), or `make reset` then `make demo`", err)
		}
		if err != nil {
			return err
		}
		keys := map[string]string{}
		for _, k := range []struct{ name, scope string }{{"demo-service", "runtime"}, {"demo-events", "ingest"}} {
			key, err := handlers.NewAPIKey(ctx, db, k.name, []string{k.scope}, 0)
			if err != nil {
				return err
			}
			keys[k.name] = key.Key
		}
		fmt.Printf(`
demo ready in %s

  Platforms   Demo Shop (shop: search, reco) and Market App (market: search, promo)
  Experiments every state — draft, in review, approved, rejected, running, paused,
              stopped, launched (one still ramping), archived; test users, targeting,
              a multi-business experiment, traffic and launch rollouts in progress
  AB Tuning   "Ranking weights auto-tune" (running, Bayesian) and
              "Result page size" (finished, quasi-random)
  Team        bob (admin), alice, dina, evan (editors), carol (viewer)
              sign in as one: make link user=alice
  API keys    shown once —
              runtime  demo-service  %s
              ingest   demo-events   %s

`, time.Since(start).Round(time.Second), keys["demo-service"], keys["demo-events"])
		if owner == 0 {
			fmt.Println("No owner yet: start the server (make dev-api) and open the setup link it prints, or run make claim.")
		}
	case "simulate":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		users := fs.Int("users", 20000, "simulated users")
		days := fs.Int("days", 14, "days of history (max 30)")
		platform := fs.String("platform", "shop", "platform key")
		business := fs.String("business", "search", "business key")
		seed := fs.Int64("seed", time.Now().UnixNano(), "random seed")
		if err := fs.Parse(args); err != nil {
			return err
		}
		fmt.Printf("simulating %d users over %d days...\n", *users, *days)
		st, err := simulate.Generate(ctx, db, simulate.Options{Platform: *platform, Business: *business, Users: *users, Days: *days, Seed: *seed})
		if err != nil {
			return err
		}
		fmt.Printf("wrote %d exposures and %d events\n", st.Exposures, st.Events)
		ps, err := pipeline.RunSettled(ctx, db, cmd, 0)
		if err != nil {
			return err
		}
		fmt.Printf("pipeline: %d assignments, %d measure rows in %.1fs\n", ps.Assignments, ps.Rows, ps.Seconds)
	case "reset":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		yes := fs.Bool("yes", false, "really drop everything")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if !*yes {
			return errors.New("reset drops every table and all data in this database; run `libra reset -yes` to confirm")
		}
		if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
			return fmt.Errorf("drop: %w", err)
		}
		if err := database.Migrate(db); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		fmt.Println("dropped everything and recreated an empty Libra database")
	case "demo-tuning":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		algorithm := fs.String("algorithm", "bayesian", "random, quasi_random, bayesian or constrained")
		rounds := fs.Int("rounds", 6, "finished rounds to play (one more stays running)")
		users := fs.Int("users", 30000, "simulated users per round")
		seed := fs.Int64("seed", time.Now().UnixNano()%1_000_000, "random seed")
		if err := fs.Parse(args); err != nil {
			return err
		}
		var owner int64
		_ = db.QueryRow(`SELECT id FROM users WHERE is_owner`).Scan(&owner)
		id, err := simulate.SeedTuning(ctx, db, owner, simulate.TuningOptions{Algorithm: *algorithm, Rounds: *rounds, Users: *users, Seed: *seed,
			Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }})
		if err != nil {
			return err
		}
		fmt.Printf("tuning study %d is running round %d — open AB Tuning in the app\n", id, *rounds+1)
	default:
		return errors.New(usage)
	}
	return nil
}
