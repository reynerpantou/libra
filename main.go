package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Minimal end-to-end experiment config platform (single-file reference) in Go.
// Features added per request:
//   - Host filter: experiment applies only if request host matches experiment.host_filter (exact match)
//   - Traffic volume (0.0 - 100.0): percentage gating before variant bucketing
//   - Variant assignment by user_id: deterministic hashing into weighted variants
//   - Dynamic nested JSON merge with per-field precedence:
//     higher layer wins; if same layer, older started_at wins; if tie, smaller exp_id wins
//
// Components:
//  1. Postgres (source of truth): namespaces, experiments, variants
//  2. Redis (distribution cache): versioned snapshots + per-variant flattened claims blob + path writers index
//  3. HTTP APIs:
//     - Control plane: create namespace, create experiment, create variant, start/pause, compile
//     - Runtime: resolve config for (namespace, host, user_id, attrs)
//     - Debug: explain a path (who writes it and who wins)
//
// Dependencies:
//
//	go get github.com/jackc/pgx/v5/pgxpool
//	go get github.com/redis/go-redis/v9
//
// Env vars:
//
//	PG_DSN=postgres://user:pass@localhost:5432/expdb?sslmode=disable
//	REDIS_ADDR=localhost:6379
//	REDIS_PASS=
//	HTTP_ADDR=:8080
//
// Run:
//
//	go run main.go
//
// Quick start (example):
//
//	curl -X POST localhost:8080/admin/migrate
//	curl -X POST localhost:8080/namespaces -d '{"name":"search/product/v5"}'
//	curl -X POST localhost:8080/experiments -d '{
//	  "namespace":"search/product/v5","name":"AB_formula","layer":0,"host_filter":"search-engine",
//	  "traffic":50.0,"started_at":"2026-01-01T00:00:00Z","salt":"AB_formula_v1"
//	}'
//	curl -X POST localhost:8080/variants -d '{
//	  "experiment_id":1,"name":"control","weight":5000,"patch_json":{"data":{"enable_formula":false}}
//	}'
//	curl -X POST localhost:8080/variants -d '{
//	  "experiment_id":1,"name":"treatment","weight":5000,"patch_json":{"data":{"enable_formula":true,"formula":"ctr*cvr"}}
//	}'
//	curl -X POST localhost:8080/experiments/1/start
//	curl -X POST localhost:8080/admin/compile -d '{"namespace":"search/product/v5"}'
//	curl -X POST localhost:8080/resolve -d '{
//	  "namespace":"search/product/v5","host":"search-engine","user_id":"123"
//	}'
package main

import (
"bytes"
"compress/gzip"
"context"
"crypto/sha1"
"encoding/hex"
"encoding/json"
"errors"
"fmt"
"io"
"log"
"math"
"net/http"
"os"
"sort"
"strconv"
"strings"
"sync"
"time"

"github.com/jackc/pgx/v5/pgxpool"
"github.com/redis/go-redis/v9"
)

type Layer int

const (
	LayerStandard      Layer = iota // 0
	LayerAboveStandard              // 1
	LayerOverride                   // 2
)

type Namespace struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	CurrentVersion int64     `json:"current_version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Experiment struct {
	ID          int64           `json:"id"`
	NamespaceID int64           `json:"namespace_id"`
	Name        string          `json:"name"`
	Layer       Layer           `json:"layer"`
	StartedAt   time.Time       `json:"started_at"`  // older wins within same layer
	Status      string          `json:"status"`      // draft|running|paused|ended
	HostFilter  string          `json:"host_filter"` // exact match; empty means all
	Traffic     float64         `json:"traffic"`     // 0.0 - 100.0 percent
	Targeting   json.RawMessage `json:"targeting"`   // optional; not fully implemented
	Salt        string          `json:"salt"`        // for bucketing
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Variant struct {
	ID           int64           `json:"id"`
	ExperimentID int64           `json:"experiment_id"`
	Name         string          `json:"name"`
	Weight       int             `json:"weight"` // sum per exp; e.g. 10000
	PatchJSON    json.RawMessage `json:"patch_json"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ===== Redis compiled formats =====

type CompiledExperiment struct {
	ExpID      int64           `json:"exp_id"`
	Name       string          `json:"name"`
	Layer      Layer           `json:"layer"`
	StartedAt  int64           `json:"started_at_unix"`
	Status     string          `json:"status"`
	HostFilter string          `json:"host_filter"`
	Traffic    float64         `json:"traffic"`
	Targeting  json.RawMessage `json:"targeting"`
	Salt       string          `json:"salt"`
	Variants   []CompiledVar   `json:"variants"`
	UpdatedAt  int64           `json:"updated_at_unix"`
}

type CompiledVar struct {
	VarID  int64  `json:"var_id"`
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}

type ClaimMeta struct {
	ExpID     int64  `json:"exp_id"`
	VarID     int64  `json:"var_id"`
	Layer     Layer  `json:"layer"`
	StartedAt int64  `json:"started_at_unix"`
	ExpName   string `json:"exp_name"`
	VarName   string `json:"var_name"`
}

type WriterClaim struct {
	Meta  ClaimMeta       `json:"meta"`
	Value json.RawMessage `json:"value"`
	Why   string          `json:"why,omitempty"` // winner reason for UI
}

type PathExplain struct {
	Namespace string        `json:"namespace"`
	Version   int64         `json:"version"`
	Path      string        `json:"path"`
	Winner    WriterClaim   `json:"winner"`
	Claims    []WriterClaim `json:"claims"`
	Conflict  bool          `json:"conflict"`
}

type ResolveRequest struct {
	Namespace string          `json:"namespace"`
	Host      string          `json:"host"`
	UserID    string          `json:"user_id"`
	Attrs     json.RawMessage `json:"attrs,omitempty"`
	Debug     bool            `json:"debug,omitempty"`
}

type ResolveResponse struct {
	Namespace string         `json:"namespace"`
	Version   int64          `json:"version"`
	Config    map[string]any `json:"config"`
	Debug     *ResolveDebug  `json:"debug,omitempty"`
}

type ResolveDebug struct {
	Assignments []Assignment             `json:"assignments"`
	Decisions   map[string]FieldDecision `json:"decisions,omitempty"` // included if Debug=true
	Conflicts   []FieldDecision          `json:"conflicts,omitempty"`
}

type Assignment struct {
	ExpID      int64   `json:"exp_id"`
	ExpName    string  `json:"exp_name"`
	Layer      Layer   `json:"layer"`
	StartedAt  int64   `json:"started_at_unix"`
	HostFilter string  `json:"host_filter"`
	Traffic    float64 `json:"traffic"`
	VarID      int64   `json:"var_id"`
	VarName    string  `json:"var_name"`
}

type Claim struct {
	Meta ClaimMeta
	Val  any
}

type FieldDecision struct {
	Path       string      `json:"path"`
	Winner     ClaimMeta   `json:"winner_meta"`
	IsConflict bool        `json:"is_conflict"`
	Claims     []ClaimMeta `json:"claims_meta"`
	// Values are omitted in debug decisions by default to keep payload small; can add if needed
}

type App struct {
	pg    *pgxpool.Pool
	redis *redis.Client

	cacheMu sync.RWMutex
	nsCache map[string]cachedSnapshot // namespace -> snapshot
}

type cachedSnapshot struct {
	version int64
	exps    []CompiledExperiment
	at      time.Time
}

// ===== Entry =====

func main() {
	ctx := context.Background()

	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		log.Fatal("PG_DSN is required")
	}
	redisAddr := getenv("REDIS_ADDR", "localhost:6379")
	redisPass := getenv("REDIS_PASS", "")
	httpAddr := getenv("HTTP_ADDR", ":8080")

	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pg connect: %v", err)
	}
	rd := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPass,
		DB:       0,
	})

	app := &App{
		pg:      pg,
		redis:   rd,
		nsCache: make(map[string]cachedSnapshot),
	}

	mux := http.NewServeMux()

	// Admin
	mux.HandleFunc("/admin/migrate", app.handleMigrate)
	mux.HandleFunc("/admin/compile", app.handleCompileNow)

	// Control plane
	mux.HandleFunc("/namespaces", app.handleNamespaces)
	mux.HandleFunc("/experiments", app.handleExperiments)
	mux.HandleFunc("/variants", app.handleVariants)
	mux.HandleFunc("/experiments/", app.handleExperimentActions) // /experiments/{id}/start|pause|end

	// Runtime
	mux.HandleFunc("/resolve", app.handleResolve)

	// Debug
	mux.HandleFunc("/debug/explain", app.handleExplainPath)

	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           withJSON(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("listening on %s", httpAddr)
	log.Fatal(srv.ListenAndServe())
}

// ===== HTTP helpers =====

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(body, dst)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ===== Migration =====

func (a *App) handleMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST only"})
		return
	}
	ctx := r.Context()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS namespaces (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			current_version BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE TABLE IF NOT EXISTS experiments (
			id BIGSERIAL PRIMARY KEY,
			namespace_id BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			layer INT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			status TEXT NOT NULL DEFAULT 'draft',
			host_filter TEXT NOT NULL DEFAULT '',
			traffic DOUBLE PRECISION NOT NULL DEFAULT 100.0,
			targeting JSONB NOT NULL DEFAULT '{}'::jsonb,
			salt TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_experiments_ns_status ON experiments(namespace_id, status);`,
		`CREATE TABLE IF NOT EXISTS variants (
			id BIGSERIAL PRIMARY KEY,
			experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			weight INT NOT NULL,
			patch_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_variants_exp ON variants(experiment_id);`,
	}

	for _, s := range stmts {
		if _, err := a.pg.Exec(ctx, s); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ===== Control plane: namespaces =====

func (a *App) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeJSON(w, 400, map[string]any{"error": "invalid body; need name"})
			return
		}
		var id int64
		err := a.pg.QueryRow(ctx,
			`INSERT INTO namespaces(name) VALUES($1)
			 ON CONFLICT(name) DO UPDATE SET updated_at=NOW()
			 RETURNING id`, req.Name).Scan(&id)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "name": req.Name})
	case http.MethodGet:
		rows, err := a.pg.Query(ctx, `SELECT id,name,current_version,updated_at FROM namespaces ORDER BY id DESC LIMIT 200`)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		defer rows.Close()
		var out []Namespace
		for rows.Next() {
			var n Namespace
			if err := rows.Scan(&n.ID, &n.Name, &n.CurrentVersion, &n.UpdatedAt); err != nil {
				writeJSON(w, 500, map[string]any{"error": err.Error()})
				return
			}
			out = append(out, n)
		}
		writeJSON(w, 200, out)
	default:
		writeJSON(w, 405, map[string]any{"error": "unsupported"})
	}
}

// ===== Control plane: experiments =====

func (a *App) handleExperiments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Namespace  string          `json:"namespace"`
			Name       string          `json:"name"`
			Layer      int             `json:"layer"`
			StartedAt  string          `json:"started_at"`
			Status     string          `json:"status,omitempty"`
			HostFilter string          `json:"host_filter,omitempty"`
			Traffic    float64         `json:"traffic"`
			Targeting  json.RawMessage `json:"targeting,omitempty"`
			Salt       string          `json:"salt,omitempty"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		if req.Namespace == "" || req.Name == "" {
			writeJSON(w, 400, map[string]any{"error": "namespace and name required"})
			return
		}
		if req.Traffic < 0.0 || req.Traffic > 100.0 || math.IsNaN(req.Traffic) {
			writeJSON(w, 400, map[string]any{"error": "traffic must be 0.0..100.0"})
			return
		}
		st := time.Now().UTC()
		if req.StartedAt != "" {
			t, err := time.Parse(time.RFC3339, req.StartedAt)
			if err != nil {
				writeJSON(w, 400, map[string]any{"error": "started_at must be RFC3339"})
				return
			}
			st = t.UTC()
		}
		status := "draft"
		if req.Status != "" {
			status = req.Status
		}
		if req.Targeting == nil {
			req.Targeting = json.RawMessage(`{}`)
		}
		// ensure namespace
		var nsID int64
		if err := a.pg.QueryRow(ctx, `SELECT id FROM namespaces WHERE name=$1`, req.Namespace).Scan(&nsID); err != nil {
			writeJSON(w, 400, map[string]any{"error": "namespace not found"})
			return
		}
		var id int64
		err := a.pg.QueryRow(ctx,
			`INSERT INTO experiments(namespace_id,name,layer,started_at,status,host_filter,traffic,targeting,salt)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 RETURNING id`,
			nsID, req.Name, req.Layer, st, status, req.HostFilter, req.Traffic, req.Targeting, req.Salt,
		).Scan(&id)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		_ = a.bumpNamespaceVersion(ctx, nsID) // change triggers compile later
		writeJSON(w, 200, map[string]any{"id": id})
	case http.MethodGet:
		ns := r.URL.Query().Get("namespace")
		if ns == "" {
			writeJSON(w, 400, map[string]any{"error": "namespace query required"})
			return
		}
		var nsID int64
		if err := a.pg.QueryRow(ctx, `SELECT id FROM namespaces WHERE name=$1`, ns).Scan(&nsID); err != nil {
			writeJSON(w, 400, map[string]any{"error": "namespace not found"})
			return
		}
		rows, err := a.pg.Query(ctx, `SELECT id,namespace_id,name,layer,started_at,status,host_filter,traffic,targeting,salt,created_at,updated_at
			FROM experiments WHERE namespace_id=$1 ORDER BY id DESC LIMIT 500`, nsID)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		defer rows.Close()
		var out []Experiment
		for rows.Next() {
			var e Experiment
			if err := rows.Scan(&e.ID, &e.NamespaceID, &e.Name, &e.Layer, &e.StartedAt, &e.Status, &e.HostFilter, &e.Traffic, &e.Targeting, &e.Salt, &e.CreatedAt, &e.UpdatedAt); err != nil {
				writeJSON(w, 500, map[string]any{"error": err.Error()})
				return
			}
			out = append(out, e)
		}
		writeJSON(w, 200, out)
	default:
		writeJSON(w, 405, map[string]any{"error": "unsupported"})
	}
}

func (a *App) handleExperimentActions(w http.ResponseWriter, r *http.Request) {
	// /experiments/{id}/start or /pause or /end
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST only"})
		return
	}
	ctx := r.Context()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "experiments" {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid id"})
		return
	}
	action := parts[2]
	var status string
	switch action {
	case "start":
		status = "running"
	case "pause":
		status = "paused"
	case "end":
		status = "ended"
	default:
		writeJSON(w, 404, map[string]any{"error": "unknown action"})
		return
	}
	var nsID int64
	err = a.pg.QueryRow(ctx, `UPDATE experiments SET status=$1, updated_at=NOW() WHERE id=$2 RETURNING namespace_id`, status, id).Scan(&nsID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	_ = a.bumpNamespaceVersion(ctx, nsID)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ===== Control plane: variants =====

func (a *App) handleVariants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodPost:
		var req struct {
			ExperimentID int64           `json:"experiment_id"`
			Name         string          `json:"name"`
			Weight       int             `json:"weight"`
			PatchJSON    json.RawMessage `json:"patch_json"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		if req.ExperimentID == 0 || req.Name == "" || req.Weight <= 0 {
			writeJSON(w, 400, map[string]any{"error": "experiment_id, name, weight required"})
			return
		}
		if req.PatchJSON == nil {
			req.PatchJSON = json.RawMessage(`{}`)
		}
		// validate JSON object
		var tmp any
		if err := json.Unmarshal(req.PatchJSON, &tmp); err != nil {
			writeJSON(w, 400, map[string]any{"error": "patch_json invalid"})
			return
		}
		if _, ok := tmp.(map[string]any); !ok {
			writeJSON(w, 400, map[string]any{"error": "patch_json root must be object"})
			return
		}

		var id int64
		err := a.pg.QueryRow(ctx,
			`INSERT INTO variants(experiment_id,name,weight,patch_json) VALUES($1,$2,$3,$4) RETURNING id`,
			req.ExperimentID, req.Name, req.Weight, req.PatchJSON,
		).Scan(&id)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		// bump namespace version of experiment
		var nsID int64
		if err := a.pg.QueryRow(ctx, `SELECT namespace_id FROM experiments WHERE id=$1`, req.ExperimentID).Scan(&nsID); err == nil {
			_ = a.bumpNamespaceVersion(ctx, nsID)
		}
		writeJSON(w, 200, map[string]any{"id": id})
	case http.MethodGet:
		expIDStr := r.URL.Query().Get("experiment_id")
		expID, _ := strconv.ParseInt(expIDStr, 10, 64)
		if expID == 0 {
			writeJSON(w, 400, map[string]any{"error": "experiment_id query required"})
			return
		}
		rows, err := a.pg.Query(ctx, `SELECT id,experiment_id,name,weight,patch_json,created_at,updated_at FROM variants WHERE experiment_id=$1 ORDER BY id ASC`, expID)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		defer rows.Close()
		var out []Variant
		for rows.Next() {
			var v Variant
			if err := rows.Scan(&v.ID, &v.ExperimentID, &v.Name, &v.Weight, &v.PatchJSON, &v.CreatedAt, &v.UpdatedAt); err != nil {
				writeJSON(w, 500, map[string]any{"error": err.Error()})
				return
			}
			out = append(out, v)
		}
		writeJSON(w, 200, out)
	default:
		writeJSON(w, 405, map[string]any{"error": "unsupported"})
	}
}

// ===== Namespace version bump =====

func (a *App) bumpNamespaceVersion(ctx context.Context, nsID int64) error {
	_, err := a.pg.Exec(ctx, `UPDATE namespaces SET current_version=current_version+1, updated_at=NOW() WHERE id=$1`, nsID)
	return err
}

// ===== Compiler =====

func (a *App) handleCompileNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST only"})
		return
	}
	var req struct {
		Namespace string `json:"namespace"`
	}
	if err := readJSON(r, &req); err != nil || req.Namespace == "" {
		writeJSON(w, 400, map[string]any{"error": "need {namespace}"})
		return
	}
	ctx := r.Context()
	ver, err := a.compileNamespace(ctx, req.Namespace)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	// invalidate local cache
	a.cacheMu.Lock()
	delete(a.nsCache, req.Namespace)
	a.cacheMu.Unlock()

	writeJSON(w, 200, map[string]any{"ok": true, "version": ver})
}

func (a *App) compileNamespace(ctx context.Context, namespace string) (int64, error) {
	// Read namespace version + id
	var nsID int64
	var ver int64
	err := a.pg.QueryRow(ctx, `SELECT id,current_version FROM namespaces WHERE name=$1`, namespace).Scan(&nsID, &ver)
	if err != nil {
		return 0, fmt.Errorf("namespace not found")
	}

	// Load running experiments
	rows, err := a.pg.Query(ctx, `SELECT id,name,layer,started_at,status,host_filter,traffic,targeting,salt,updated_at
		FROM experiments WHERE namespace_id=$1 AND status='running'`, nsID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var compiled []CompiledExperiment
	for rows.Next() {
		var eID int64
		var name string
		var layer int
		var startedAt time.Time
		var status string
		var hostFilter string
		var traffic float64
		var targeting json.RawMessage
		var salt string
		var updatedAt time.Time
		if err := rows.Scan(&eID, &name, &layer, &startedAt, &status, &hostFilter, &traffic, &targeting, &salt, &updatedAt); err != nil {
			return 0, err
		}

		// variants
		vrows, err := a.pg.Query(ctx, `SELECT id,name,weight,patch_json FROM variants WHERE experiment_id=$1 ORDER BY id ASC`, eID)
		if err != nil {
			return 0, err
		}
		var vars []CompiledVar
		var variantPatches []struct {
			varID  int64
			name   string
			patch  json.RawMessage
			weight int
		}
		for vrows.Next() {
			var vid int64
			var vname string
			var wgt int
			var patch json.RawMessage
			if err := vrows.Scan(&vid, &vname, &wgt, &patch); err != nil {
				vrows.Close()
				return 0, err
			}
			vars = append(vars, CompiledVar{VarID: vid, Name: vname, Weight: wgt})
			variantPatches = append(variantPatches, struct {
				varID  int64
				name   string
				patch  json.RawMessage
				weight int
			}{varID: vid, name: vname, patch: patch, weight: wgt})
		}
		vrows.Close()

		ce := CompiledExperiment{
			ExpID:      eID,
			Name:       name,
			Layer:      Layer(layer),
			StartedAt:  startedAt.UTC().Unix(),
			Status:     status,
			HostFilter: hostFilter,
			Traffic:    traffic,
			Targeting:  targeting,
			Salt:       salt,
			Variants:   vars,
			UpdatedAt:  updatedAt.UTC().Unix(),
		}
		compiled = append(compiled, ce)

		// Build and write per-variant claims blob and optional pathwriters
		for _, vp := range variantPatches {
			flat, err := flattenJSONToLeaves(vp.patch)
			if err != nil {
				return 0, fmt.Errorf("flatten exp %d var %d: %w", eID, vp.varID, err)
			}
			blob, err := marshalAndGzip(flat)
			if err != nil {
				return 0, err
			}
			key := redisVarBlobKey(namespace, ver, eID, vp.varID)
			if err := a.redis.Set(ctx, key, blob, 0).Err(); err != nil {
				return 0, err
			}

			// build path writers index as a Set per path (simple and useful)
			for path := range flat {
				pwKey := redisPathWritersKey(namespace, ver, path)
				member := fmt.Sprintf("%d:%d", eID, vp.varID)
				if err := a.redis.SAdd(ctx, pwKey, member).Err(); err != nil {
					return 0, err
				}
			}
		}
	}

	// snapshot key
	snapKey := redisSnapshotKey(namespace, ver)
	snapBytes, _ := json.Marshal(compiled)
	if err := a.redis.Set(ctx, snapKey, snapBytes, 0).Err(); err != nil {
		return 0, err
	}
	// latest pointer
	if err := a.redis.Set(ctx, redisLatestKey(namespace), strconv.FormatInt(ver, 10), 0).Err(); err != nil {
		return 0, err
	}
	return ver, nil
}

// ===== Runtime resolve =====

func (a *App) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "POST only"})
		return
	}
	var req ResolveRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if req.Namespace == "" || req.UserID == "" {
		writeJSON(w, 400, map[string]any{"error": "namespace and user_id required"})
		return
	}
	ctx := r.Context()

	ver, exps, err := a.getSnapshot(ctx, req.Namespace)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	assignments := make([]Assignment, 0)
	chosen := make([]struct {
		exp     CompiledExperiment
		varID   int64
		varName string
	}, 0)

	for _, e := range exps {
		// host filter
		if e.HostFilter != "" && req.Host != "" && e.HostFilter != req.Host {
			continue
		}
		if e.HostFilter != "" && req.Host == "" {
			// if host filter is set but request doesn't supply host, we consider it not eligible
			continue
		}

		// traffic gating
		if !passesTraffic(e, req.UserID) {
			continue
		}

		vid, vname, ok := pickVariant(e, req.UserID)
		if !ok {
			continue
		}
		assignments = append(assignments, Assignment{
			ExpID: e.ExpID, ExpName: e.Name, Layer: e.Layer, StartedAt: e.StartedAt,
			HostFilter: e.HostFilter, Traffic: e.Traffic,
			VarID: vid, VarName: vname,
		})
		chosen = append(chosen, struct {
			exp     CompiledExperiment
			varID   int64
			varName string
		}{exp: e, varID: vid, varName: vname})
	}

	// Merge chosen variants by per-field precedence
	type winner struct {
		meta ClaimMeta
		val  any
	}
	winners := map[string]winner{}
	decisions := map[string]FieldDecision{} // only if debug
	conflicts := make([]FieldDecision, 0)

	for _, c := range chosen {
		key := redisVarBlobKey(req.Namespace, ver, c.exp.ExpID, c.varID)
		blob, err := a.redis.Get(ctx, key).Bytes()
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": fmt.Sprintf("missing compiled variant blob: %v", err)})
			return
		}
		flat, err := ungzipAndUnmarshal(blob)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		meta := ClaimMeta{
			ExpID: c.exp.ExpID, VarID: c.varID,
			Layer: c.exp.Layer, StartedAt: c.exp.StartedAt,
			ExpName: c.exp.Name, VarName: c.varName,
		}

		for path, val := range flat {
			if cur, ok := winners[path]; !ok {
				winners[path] = winner{meta: meta, val: val}
				if req.Debug {
					decisions[path] = FieldDecision{
						Path: path, Winner: meta, IsConflict: false, Claims: []ClaimMeta{meta},
					}
				}
				continue
			} else {
				// record claim for debug
				if req.Debug {
					fd := decisions[path]
					fd.Claims = append(fd.Claims, meta)
					decisions[path] = fd
				}

				// choose better claim
				if better(meta, cur.meta) {
					winners[path] = winner{meta: meta, val: val}
					if req.Debug {
						fd := decisions[path]
						fd.Winner = meta
						decisions[path] = fd
					}
				}
			}
		}
	}

	if req.Debug {
		// mark conflicts
		for path, fd := range decisions {
			if len(fd.Claims) > 1 {
				fd.IsConflict = true
				decisions[path] = fd
				conflicts = append(conflicts, fd)
			}
		}
		sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
	}

	merged, err := unflattenWinners(winners)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	resp := ResolveResponse{
		Namespace: req.Namespace,
		Version:   ver,
		Config:    merged,
	}
	if req.Debug {
		resp.Debug = &ResolveDebug{
			Assignments: assignments,
			Decisions:   decisions,
			Conflicts:   conflicts,
		}
	}
	writeJSON(w, 200, resp)
}

// ===== Debug explain =====

func (a *App) handleExplainPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"error": "GET only"})
		return
	}
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	path := r.URL.Query().Get("path")
	host := r.URL.Query().Get("host")
	userID := r.URL.Query().Get("user_id")
	if ns == "" || path == "" {
		writeJSON(w, 400, map[string]any{"error": "need namespace and path"})
		return
	}

	ver, exps, err := a.getSnapshot(ctx, ns)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	// Find all variant writers for this path from redis set
	pwKey := redisPathWritersKey(ns, ver, path)
	members, err := a.redis.SMembers(ctx, pwKey).Result()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	// Build lookup for exp metadata + var name
	expByID := map[int64]CompiledExperiment{}
	varName := map[[2]int64]string{}
	for _, e := range exps {
		expByID[e.ExpID] = e
		for _, v := range e.Variants {
			varName[[2]int64{e.ExpID, v.VarID}] = v.Name
		}
	}

	claims := make([]WriterClaim, 0, len(members))
	for _, m := range members {
		parts := strings.Split(m, ":")
		if len(parts) != 2 {
			continue
		}
		eid, _ := strconv.ParseInt(parts[0], 10, 64)
		vid, _ := strconv.ParseInt(parts[1], 10, 64)
		e, ok := expByID[eid]
		if !ok {
			continue
		}
		// optional: apply host filter and traffic for this "explain" if host+user_id provided
		if host != "" {
			if e.HostFilter != "" && e.HostFilter != host {
				continue
			}
		}
		if userID != "" {
			if !passesTraffic(e, userID) {
				continue
			}
			// optional: ensure this user actually would be assigned to this variant
			pVid, _, ok := pickVariant(e, userID)
			if !ok || pVid != vid {
				continue
			}
		}

		// get the value for that path (we read blob and extract just that path)
		blob, err := a.redis.Get(ctx, redisVarBlobKey(ns, ver, eid, vid)).Bytes()
		if err != nil {
			continue
		}
		flat, err := ungzipAndUnmarshal(blob)
		if err != nil {
			continue
		}
		val, ok := flat[path]
		if !ok {
			continue
		}
		valJSON, _ := json.Marshal(val)

		claims = append(claims, WriterClaim{
			Meta: ClaimMeta{
				ExpID: eid, VarID: vid, Layer: e.Layer, StartedAt: e.StartedAt,
				ExpName: e.Name, VarName: varName[[2]int64{eid, vid}],
			},
			Value: valJSON,
		})
	}

	if len(claims) == 0 {
		writeJSON(w, 200, PathExplain{
			Namespace: ns, Version: ver, Path: path,
			Conflict: false,
			Claims:   []WriterClaim{},
		})
		return
	}

	// pick winner
	sort.SliceStable(claims, func(i, j int) bool {
		ai, aj := claims[i].Meta, claims[j].Meta
		if ai.Layer != aj.Layer {
			return ai.Layer > aj.Layer
		}
		if ai.StartedAt != aj.StartedAt {
			return ai.StartedAt < aj.StartedAt
		}
		if ai.ExpID != aj.ExpID {
			return ai.ExpID < aj.ExpID
		}
		return ai.VarID < aj.VarID
	})
	winner := claims[0]
	winner.Why = winnerReason(winner.Meta, claims)

	writeJSON(w, 200, PathExplain{
		Namespace: ns,
		Version:   ver,
		Path:      path,
		Winner:    winner,
		Claims:    claims,
		Conflict:  len(claims) > 1,
	})
}

func winnerReason(win ClaimMeta, claims []WriterClaim) string {
	// Explain based on the best competitor (2nd place)
	if len(claims) <= 1 {
		return "only writer"
	}
	other := claims[1].Meta
	if win.Layer != other.Layer {
		return fmt.Sprintf("won by layer: %d > %d", win.Layer, other.Layer)
	}
	if win.StartedAt != other.StartedAt {
		return fmt.Sprintf("same layer; won by earlier started_at: %d < %d", win.StartedAt, other.StartedAt)
	}
	if win.ExpID != other.ExpID {
		return fmt.Sprintf("same layer/time; won by smaller exp_id: %d < %d", win.ExpID, other.ExpID)
	}
	return "tie-break"
}

// ===== Snapshot cache =====

func (a *App) getSnapshot(ctx context.Context, namespace string) (int64, []CompiledExperiment, error) {
	// local cache TTL
	const ttl = 30 * time.Second

	a.cacheMu.RLock()
	if snap, ok := a.nsCache[namespace]; ok && time.Since(snap.at) < ttl {
		a.cacheMu.RUnlock()
		return snap.version, snap.exps, nil
	}
	a.cacheMu.RUnlock()

	// get latest version
	verStr, err := a.redis.Get(ctx, redisLatestKey(namespace)).Result()
	if err != nil {
		return 0, nil, fmt.Errorf("no compiled namespace (run /admin/compile): %v", err)
	}
	ver, _ := strconv.ParseInt(verStr, 10, 64)

	// get snapshot
	b, err := a.redis.Get(ctx, redisSnapshotKey(namespace, ver)).Bytes()
	if err != nil {
		return 0, nil, fmt.Errorf("missing snapshot for version %d: %v", ver, err)
	}
	var exps []CompiledExperiment
	if err := json.Unmarshal(b, &exps); err != nil {
		return 0, nil, err
	}
	// cache
	a.cacheMu.Lock()
	a.nsCache[namespace] = cachedSnapshot{version: ver, exps: exps, at: time.Now()}
	a.cacheMu.Unlock()

	return ver, exps, nil
}

// ===== Eligibility: host + traffic =====

func passesTraffic(e CompiledExperiment, userID string) bool {
	// traffic is 0..100
	if e.Traffic <= 0.0 {
		return false
	}
	if e.Traffic >= 100.0 {
		return true
	}
	// deterministic hash to [0, 100)
	h := hashToUint32(userID + "|traffic|" + strconv.FormatInt(e.ExpID, 10) + "|" + e.Salt)
	p := float64(h%100000) / 1000.0 // 0..99.999
	return p < e.Traffic
}

func pickVariant(e CompiledExperiment, userID string) (int64, string, bool) {
	if len(e.Variants) == 0 {
		return 0, "", false
	}
	total := 0
	for _, v := range e.Variants {
		if v.Weight > 0 {
			total += v.Weight
		}
	}
	if total <= 0 {
		return 0, "", false
	}
	h := hashToUint32(userID + "|bucket|" + strconv.FormatInt(e.ExpID, 10) + "|" + e.Salt)
	bucket := int(h % uint32(total))
	acc := 0
	for _, v := range e.Variants {
		if v.Weight <= 0 {
			continue
		}
		acc += v.Weight
		if bucket < acc {
			return v.VarID, v.Name, true
		}
	}
	// fallback
	return e.Variants[len(e.Variants)-1].VarID, e.Variants[len(e.Variants)-1].Name, true
}

// Precedence: higher layer wins; if same layer, older started_at wins; if tie, smaller exp_id wins; if tie, smaller var_id
func better(a, b ClaimMeta) bool {
	if a.Layer != b.Layer {
		return a.Layer > b.Layer
	}
	if a.StartedAt != b.StartedAt {
		return a.StartedAt < b.StartedAt
	}
	if a.ExpID != b.ExpID {
		return a.ExpID < b.ExpID
	}
	return a.VarID < b.VarID
}

// ===== Flatten / Unflatten =====

func flattenJSONToLeaves(patch json.RawMessage) (map[string]any, error) {
	var root any
	if err := json.Unmarshal(patch, &root); err != nil {
		return nil, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("root must be object")
	}
	out := map[string]any{}
	flatten("", obj, out)
	return out, nil
}

// flatten converts nested objects into dot-path leaf assignments.
// Arrays are treated as atomic values (winner takes whole array).
func flatten(prefix string, v any, out map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(p, vv, out)
		}
	case []any:
		out[prefix] = x
	default:
		out[prefix] = x
	}
}

func unflattenWinners(winners map[string]struct {
	meta ClaimMeta
	val  any
}) (map[string]any, error) {
	root := map[string]any{}
	for path, w := range winners {
		if err := setByPath(root, path, w.val); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func setByPath(root map[string]any, path string, value any) error {
	if path == "" {
		return errors.New("empty path")
	}
	parts := strings.Split(path, ".")
	cur := root
	for i := 0; i < len(parts); i++ {
		key := parts[i]
		last := i == len(parts)-1
		if last {
			cur[key] = value
			return nil
		}
		nxt, ok := cur[key]
		if !ok {
			nm := map[string]any{}
			cur[key] = nm
			cur = nm
			continue
		}
		obj, ok := nxt.(map[string]any)
		if !ok {
			return fmt.Errorf("path conflict at %q: existing type %T blocks nested set", strings.Join(parts[:i+1], "."), nxt)
		}
		cur = obj
	}
	return nil
}

// ===== Compression helpers =====

func marshalAndGzip(m map[string]any) ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ungzipAndUnmarshal(blob []byte) (map[string]any, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, 25<<20))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ===== Redis keys =====

func redisLatestKey(ns string) string {
	return "ns:" + ns + ":latest"
}
func redisSnapshotKey(ns string, ver int64) string {
	return fmt.Sprintf("ns:%s:v:%d:snapshot", ns, ver)
}
func redisVarBlobKey(ns string, ver int64, expID int64, varID int64) string {
	return fmt.Sprintf("ns:%s:v:%d:exp:%d:var:%d:blob", ns, ver, expID, varID)
}
func redisPathWritersKey(ns string, ver int64, path string) string {
	// path may be long; hash it to keep key short, but keep original path in debug by query param
	sum := sha1.Sum([]byte(path))
	return fmt.Sprintf("ns:%s:v:%d:pathwriters:%s", ns, ver, hex.EncodeToString(sum[:8]))
}

// ===== Hashing =====

func hashToUint32(s string) uint32 {
	h := sha1.Sum([]byte(s))
	// use first 4 bytes
	return uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
}

// ===== (Optional) RNG seeding for any future non-deterministic ops =====
func init() {
	// deterministic operations use sha1-based hashing; rand kept for future utilities
	randSeed := time.Now().UnixNano()
	_ = randSeed
}

// ===== Notes =====
// - Targeting is stored but not evaluated here. Add a small targeting DSL if needed.
// - Host filter is exact match for simplicity; you can upgrade to suffix/pattern matching safely.
// - Path writers index uses a hashed key. The /debug/explain reads assignments and values by extracting from blob.
// - Compiler currently overwrites/accumulates path writer sets per version without deletion. In production,
//   you'd write into new version keys only (which we do) and let old versions expire or be GC'd.
// - Add Redis TTLs if you want to limit storage growth.

