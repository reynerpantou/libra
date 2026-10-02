// Package handlers holds the HTTP API: the management API the web app uses
// (session auth), and the runtime API services call (API-key auth).
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/reynerpantou/libra/internal/config"
	"github.com/reynerpantou/libra/internal/middleware"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/sso"
)

type Server struct {
	DB        *sql.DB
	Cfg       config.Config
	ClientIP  func(*http.Request) string
	Providers map[string]*sso.Provider
	Store     *serving.Store
	Exposures *serving.Logger
	Pipeline  *pipeline.Scheduler
}

func New(db *sql.DB, cfg config.Config, store *serving.Store, logger *serving.Logger, sched *pipeline.Scheduler) *Server {
	appURL = cfg.AppURL()
	s := &Server{
		DB: db, Cfg: cfg, ClientIP: middleware.ClientIP(cfg.TrustedProxies), Providers: map[string]*sso.Provider{},
		Store: store, Exposures: logger, Pipeline: sched,
	}
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" {
		s.Providers["google"] = sso.NewGoogle(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleTestBase)
	}
	if cfg.AppleClientID != "" && cfg.AppleTeamID != "" && cfg.AppleKeyID != "" && cfg.ApplePrivateKey != "" {
		p, err := sso.NewApple(cfg.AppleClientID, cfg.AppleTeamID, cfg.AppleKeyID, cfg.ApplePrivateKey, cfg.AppleTestBase)
		if err != nil {
			log.Fatalf("sign in with apple: %v", err)
		}
		s.Providers["apple"] = p
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a machine-readable code plus a human-readable message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "validation_error", message)
}

func notFound(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotFound, "not_found", what+" not found")
}

// serverError logs the cause and sends a generic 500.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "server_error", "something went wrong")
}

var errNotJSON = errors.New("request body must be application/json")

// requireJSON rejects bodies not sent as JSON, which closes cross-site form
// posts (they can't set this content type without a CORS preflight).
func requireJSON(r *http.Request) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errNotJSON
	}
	return nil
}

func decodeLimit(r *http.Request, v any, limit int64) error {
	if err := requireJSON(r); err != nil {
		return err
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func decode(r *http.Request, v any) error { return decodeLimit(r, v, 1<<20) }

// decodeOr400 decodes the body or writes a 400 and returns false.
func decodeOr400(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := decode(r, v); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return false
	}
	return true
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func user(r *http.Request) middleware.User { return middleware.UserFrom(r.Context()) }

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// audit records a change. experimentID may be 0 for non-experiment entities.
func audit(ctx context.Context, x execer, actor int64, experimentID int64, entity string, entityID int64, action, from, to string, detail any) error {
	var exp, from_, to_ any
	if experimentID > 0 {
		exp = experimentID
	}
	if from != "" {
		from_ = from
	}
	if to != "" {
		to_ = to
	}
	d := []byte("{}")
	if detail != nil {
		d, _ = json.Marshal(detail)
	}
	var actorID any
	if actor > 0 {
		actorID = actor
	}
	_, err := x.ExecContext(ctx,
		`INSERT INTO audit_log (experiment_id, entity, entity_id, actor_id, action, from_status, to_status, detail) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		exp, entity, entityID, actorID, action, from_, to_, d)
	return err
}

// isUniqueViolation reports a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// trimmed returns s trimmed and whether it's within max runes and non-empty.
func trimmed(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && len([]rune(s)) <= max
}
