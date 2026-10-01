package simulate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/tuning"
)

// TuningOptions control the AB Tuning demo.
type TuningOptions struct {
	Algorithm string // default bayesian
	Users     int    // default 30000
	Rounds    int    // finished rounds to play (default 6); one more is left running
	MaxRounds int    // default 10
	Seed      int64
	Log       func(format string, args ...any)
}

// SeedTuning creates a running tuning study on the demo search business and
// plays its first rounds in the past: each round's traffic is simulated
// against the live configuration (so units really get that round's arms),
// the pipeline runs, and the real engine analyses the round and picks the
// next candidates. It returns the study's experiment id.
func SeedTuning(ctx context.Context, db *sql.DB, ownerID int64, o TuningOptions) (int64, error) {
	if o.Algorithm == "" {
		o.Algorithm = tuning.Bayesian
	}
	if o.Users <= 0 {
		o.Users = 30000
	}
	if o.Rounds <= 0 {
		o.Rounds = 6
	}
	if o.MaxRounds <= o.Rounds {
		o.MaxRounds = o.Rounds + 4
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	var bid, ctrID, latencyID int64
	if err := db.QueryRowContext(ctx, `SELECT b.id FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE p.key = $1 AND b.key = 'search'`, demoPlatform.key).Scan(&bid); err != nil {
		return 0, fmt.Errorf("the demo search business is missing (run libra demo first): %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id FROM metrics WHERE business_id = $1 AND key = 'ctr'`, bid).Scan(&ctrID); err != nil {
		return 0, fmt.Errorf("demo metric ctr: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id FROM metrics WHERE business_id = $1 AND key = 'search_latency_ms'`, bid).Scan(&latencyID); err != nil {
		return 0, fmt.Errorf("demo metric search_latency_ms: %w", err)
	}
	cfg := tuning.Config{
		Algorithm: o.Algorithm,
		Params: tuning.Space{
			{Path: "search.ranking.relevance_weight", Type: "float", Min: 0.2, Max: 1.0, Control: 0.5},
			{Path: "search.ranking.freshness_weight", Type: "float", Min: 0.2, Max: 1.0, Control: 0.5},
		},
		BaseParams:         map[string]any{},
		ObjectiveMetricID:  ctrID,
		ObjectiveDirection: "increase",
		Guardrails:         []tuning.Guardrail{{MetricID: latencyID, MaxDrop: 0.05}},
		Arms:               10,
		RoundDays:          1,
		MaxRounds:          o.MaxRounds,
		KeepBest:           true,
	}
	if err := cfg.Params.Validate(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -o.Rounds)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var layerID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO layers (name, description, salt, diversion, auto) VALUES ($1, '', $2, 'user_id', true) RETURNING id`,
		fmt.Sprintf("auto-tune-%d", now.UnixNano()), fmt.Sprintf("tl%d", o.Seed)).Scan(&layerID); err != nil {
		return 0, err
	}
	buckets := make([]int, 1000)
	for i := range buckets {
		buckets[i] = i
	}
	salt := fmt.Sprintf("tune%x", o.Seed&0xffffffff)
	var owner any
	if ownerID > 0 {
		owner = ownerID
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO experiments (business_id, business_ids, layer_id, name, hypothesis, description, owner_id, status, salt, traffic_target, buckets,
			targeting, metric_group_ids, started_at, created_at, kind)
		VALUES ($1, ARRAY[$1::bigint], $2, $3, $4, $5, $6, 'active', $7, 1000, $8, '{"groups": []}', '{}', $9, $9, 'tuning') RETURNING id`,
		bid, layerID, "Ranking weights auto-tune",
		"Some mix of relevance and freshness weights lifts search CTR without slowing search down.",
		"Two ranking weights tuned in [0.2, 1.0]. v0 keeps today's 0.5 / 0.5.",
		owner, salt, buckets, start).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE layers SET name = $2 WHERE id = $1`, layerID, fmt.Sprintf("auto-%d", id)); err != nil {
		return 0, err
	}
	cw, _ := tuning.Weights(cfg.Arms)
	ctlParams, _ := json.Marshal(tuning.ArmParams(demoPlatform.key, cfg, cfg.Params.Controls()))
	if _, err := tx.ExecContext(ctx, `INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position) VALUES ($1, 'v0', 'v0 · control', true, $2, $3, 0)`,
		id, cw, ctlParams); err != nil {
		return 0, err
	}
	params, _ := json.Marshal(cfg.Params)
	guards, _ := json.Marshal(cfg.Guardrails)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tunings (experiment_id, algorithm, params, base_params, objective_metric_id, objective_direction, guardrails,
			arms, round_days, max_rounds, min_units, keep_best, seed, base_salt)
		VALUES ($1, $2, $3, '{}', $4, 'increase', $5, $6, $7, $8, 0, true, $9, $10)`,
		id, cfg.Algorithm, params, ctrID, guards, cfg.Arms, cfg.RoundDays, cfg.MaxRounds, o.Seed, salt); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE experiments SET salt = $2 WHERE id = $1`, id, salt+":r1"); err != nil {
		return 0, err
	}
	if err := tuning.CreateFirstRound(ctx, tx, id); err != nil {
		return 0, err
	}
	if err := tuning.StartRound(ctx, tx, id, start); err != nil {
		return 0, err
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	for r := 1; r <= o.Rounds; r++ {
		day := start.AddDate(0, 0, r-1)
		st, err := Generate(ctx, db, Options{Platform: demoPlatform.key, Business: "search", Users: o.Users, Days: 1, Start: day,
			UserPrefix: "demo-tune", Seed: o.Seed + int64(r)})
		if err != nil {
			return id, err
		}
		if _, err := pipeline.RunSettled(ctx, db, "demo", 0); err != nil {
			return id, err
		}
		out, err := tuning.Advance(ctx, db, id, day.AddDate(0, 0, 1), false, nil)
		if err != nil {
			return id, fmt.Errorf("round %d: %w", r, err)
		}
		o.Log("tuning round %d: %d exposures, %d events → %s", r, st.Exposures, st.Events, out.Note)
	}
	return id, nil
}
