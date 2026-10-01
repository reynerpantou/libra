package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/tuning"
)

// AB Tuning: an experiment whose treatment arms get new candidate values
// every round. v0 (the control) keeps today's values throughout; the
// algorithm picks each round's points from all earlier rounds' results.

type tuningRequest struct {
	experimentRequest
	Tuning tuning.Config `json:"tuning"`
}

// MetricInfo describes a metric a study measures.
type MetricInfo struct {
	ID        int64   `json:"id"`
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Format    string  `json:"format"`
	Decimals  int     `json:"decimals"`
	Direction string  `json:"direction"`
	MaxDrop   float64 `json:"max_drop,omitempty"` // guardrails
}

// TuningRound is one round, its arms and (once analysed) results.
type TuningRound struct {
	Round     int             `json:"round"`
	Status    string          `json:"status"`
	StartedAt *time.Time      `json:"started_at"`
	EndsAt    *time.Time      `json:"ends_at"`
	EndedAt   *time.Time      `json:"ended_at"`
	Arms      []tuning.Arm    `json:"arms"`
	Results   *tuning.Results `json:"results"`
	Note      string          `json:"note"`
}

// TuningBest is the recommendation: a tested arm.
type TuningBest struct {
	VariantID int64             `json:"variant_id"`
	Key       string            `json:"key"`
	Round     int               `json:"round"`
	Values    []float64         `json:"values"`
	Predicted tuning.Prediction `json:"predicted"`
	Observed  float64           `json:"observed"`
	Params    map[string]any    `json:"params"`
}

// Tuning is a study as the UI sees it.
type Tuning struct {
	Experiment  Experiment    `json:"experiment"`
	Config      tuning.Config `json:"config"`
	Round       int           `json:"round"`
	State       tuning.State  `json:"state"`
	FinishedAt  *time.Time    `json:"finished_at"`
	Note        string        `json:"note"`
	Objective   MetricInfo    `json:"objective"`
	Guardrails  []MetricInfo  `json:"guardrails"`
	Rounds      []TuningRound `json:"rounds"`
	Best        *TuningBest   `json:"best"`
	Tested      int           `json:"tested"` // points measured so far
	DataThrough *time.Time    `json:"data_through"`
}

// TuningSummary is a list row.
type TuningSummary struct {
	Experiment
	Algorithm  string     `json:"algorithm"`
	Round      int        `json:"round"`
	MaxRounds  int        `json:"max_rounds"`
	Arms       int        `json:"arms"`
	Params     []string   `json:"params"`
	Objective  string     `json:"objective"`
	BestLift   *float64   `json:"best_lift"` // best observed objective lift (oriented)
	FinishedAt *time.Time `json:"finished_at"`
}

func (s *Server) metricInfo(ctx context.Context, id int64) (MetricInfo, error) {
	m := MetricInfo{ID: id}
	err := s.DB.QueryRowContext(ctx, `SELECT key, name, format, decimals, direction FROM metrics WHERE id = $1`, id).
		Scan(&m.Key, &m.Name, &m.Format, &m.Decimals, &m.Direction)
	return m, err
}

// validateTuning checks a study's setup and fills defaults.
func (s *Server) validateTuning(ctx context.Context, c *tuning.Config) string {
	if !slices.Contains(tuning.Algorithms, c.Algorithm) {
		return "choose an algorithm: random, quasi_random, bayesian or constrained"
	}
	if err := c.Params.Validate(); err != nil {
		return err.Error()
	}
	if c.BaseParams == nil {
		c.BaseParams = map[string]any{}
	}
	if c.Arms == 0 {
		c.Arms = 10
	}
	if c.Arms < 1 || c.Arms > 20 {
		return "1 to 20 treatment arms"
	}
	if c.RoundDays == 0 {
		c.RoundDays = 1
	}
	if c.RoundDays < 1 || c.RoundDays > 30 {
		return "a round lasts 1 to 30 days"
	}
	if c.MaxRounds == 0 {
		c.MaxRounds = 10
	}
	if c.MaxRounds < 1 || c.MaxRounds > 100 {
		return "1 to 100 rounds"
	}
	if c.MinUnits < 0 || c.MinUnits > 10_000_000 {
		return "minimum units per arm must be 0 to 10,000,000"
	}
	if c.ObjectiveMetricID == 0 {
		return "choose the objective metric"
	}
	m, err := s.metricInfo(ctx, c.ObjectiveMetricID)
	if err != nil {
		return "the objective metric doesn't exist"
	}
	if c.ObjectiveDirection == "" {
		c.ObjectiveDirection = m.Direction
	}
	if c.ObjectiveDirection != "increase" && c.ObjectiveDirection != "decrease" {
		return fmt.Sprintf("%s has no preferred direction: say whether to increase or decrease it", m.Name)
	}
	if len(c.Guardrails) > 10 {
		return "at most 10 guardrails"
	}
	seen := map[int64]bool{c.ObjectiveMetricID: true}
	for i, g := range c.Guardrails {
		if seen[g.MetricID] {
			return "each guardrail metric must be different from the objective and from each other"
		}
		seen[g.MetricID] = true
		if _, err := s.metricInfo(ctx, g.MetricID); err != nil {
			return fmt.Sprintf("guardrail %d: the metric doesn't exist", i+1)
		}
		if math.IsNaN(g.MaxDrop) || g.MaxDrop < 0 || g.MaxDrop > 1 {
			return "a guardrail's allowed drop is between 0% and 100%"
		}
	}
	if c.Algorithm == tuning.Constrained && len(c.Guardrails) == 0 {
		return "the constrained algorithm needs at least one guardrail"
	}
	if c.Guardrails == nil {
		c.Guardrails = []tuning.Guardrail{}
	}
	return ""
}

// prepareTuning validates the request and builds the variants the
// experiment validation expects (v0 plus placeholder arms).
func (s *Server) prepareTuning(ctx context.Context, req *tuningRequest, self int64) (string, string) {
	if msg := s.validateTuning(ctx, &req.Tuning); msg != "" {
		return "", msg
	}
	req.EndAt = nil // a study ends after its rounds; extending adds rounds
	if req.BusinessID == 0 && len(req.BusinessIDs) > 0 {
		req.BusinessID = req.BusinessIDs[0]
	}
	var platformKey string
	if err := s.DB.QueryRowContext(ctx, `SELECT p.key FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE b.id = $1`, req.BusinessID).Scan(&platformKey); err != nil {
		return "", "choose a business"
	}
	cw, aw := tuning.Weights(req.Tuning.Arms)
	req.Variants = []VariantIn{{Key: "v0", Name: "v0 · control", IsControl: true, Weight: cw,
		Params: tuning.ArmParams(platformKey, req.Tuning, req.Tuning.Params.Controls())}}
	for i := 1; i <= req.Tuning.Arms; i++ {
		req.Variants = append(req.Variants, VariantIn{Key: fmt.Sprintf("r1_v%d", i), Weight: aw, Params: map[string]any{platformKey: map[string]any{}}})
	}
	if msg := req.validate(); msg != "" {
		return "", msg
	}
	if msg := s.checkRefs(ctx, &req.experimentRequest, self); msg != "" {
		return "", msg
	}
	return platformKey, ""
}

func (s *Server) CreateTuning(w http.ResponseWriter, r *http.Request) {
	var req tuningRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if _, msg := s.prepareTuning(r.Context(), &req, 0); msg != "" {
		badRequest(w, msg)
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	id, salt, err := insertExperiment(r.Context(), tx, &req.experimentRequest, user(r).ID, "tuning")
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := writeVariants(r.Context(), tx, id, req.Variants[:1]); err != nil {
		serverError(w, r, err)
		return
	}
	if err := saveTuning(r.Context(), tx, id, req.Tuning, rand.Int63n(1<<40), salt, true); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tuning.CreateFirstRound(r.Context(), tx, id); err != nil {
		serverError(w, r, err)
		return
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "create", "", assign.StatusDraft, map[string]any{"kind": "tuning", "algorithm": req.Tuning.Algorithm}); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	s.writeTuning(w, r, id, http.StatusCreated)
}

func saveTuning(ctx context.Context, tx *sql.Tx, id int64, c tuning.Config, seed int64, salt string, insert bool) error {
	params, _ := json.Marshal(c.Params)
	base, _ := json.Marshal(c.BaseParams)
	guards, _ := json.Marshal(c.Guardrails)
	if insert {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tunings (experiment_id, algorithm, params, base_params, objective_metric_id, objective_direction, guardrails,
				arms, round_days, max_rounds, min_units, keep_best, seed, base_salt)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			id, c.Algorithm, params, base, c.ObjectiveMetricID, c.ObjectiveDirection, guards, c.Arms, c.RoundDays, c.MaxRounds, c.MinUnits, c.KeepBest, seed, salt); err != nil {
			return err
		}
		// Round 1 splits units with its own salt; later rounds re-randomise.
		_, err := tx.ExecContext(ctx, `UPDATE experiments SET salt = $2 WHERE id = $1`, id, salt+":r1")
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE tunings SET algorithm = $2, params = $3, base_params = $4, objective_metric_id = $5, objective_direction = $6, guardrails = $7,
			arms = $8, round_days = $9, max_rounds = $10, min_units = $11, keep_best = $12, state = '{}'
		WHERE experiment_id = $1`,
		id, c.Algorithm, params, base, c.ObjectiveMetricID, c.ObjectiveDirection, guards, c.Arms, c.RoundDays, c.MaxRounds, c.MinUnits, c.KeepBest)
	return err
}

// UpdateTuning edits a study that hasn't started; round 1 is planned again.
func (s *Server) UpdateTuning(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	cur, err := s.loadExperiment(r.Context(), id, false)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && cur.Kind != "tuning") {
		notFound(w, "tuning study")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	switch cur.Status {
	case assign.StatusDraft, assign.StatusApproved, assign.StatusRejected:
	case assign.StatusInReview:
		badRequest(w, "withdraw the review before editing")
		return
	default:
		badRequest(w, "a study can only be edited before it starts; clone it to run another")
		return
	}
	var req tuningRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	// The business and traffic split stay; everything else can change.
	req.BusinessID, req.BusinessIDs = cur.BusinessID, cur.BusinessIDs
	req.LayerID, req.AutoDiversion = cur.LayerID, ""
	if _, msg := s.prepareTuning(r.Context(), &req, id); msg != "" {
		badRequest(w, msg)
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	targeting, _ := json.Marshal(req.Targeting)
	// An approved study needs another review after a change.
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE experiments SET name = $2, hypothesis = $3, description = $4, traffic_target = $5, targeting = $6, metric_group_ids = $7,
			status = 'draft', review_note = '', planned_start = $8, updated_at = now()
		WHERE id = $1`, id, req.Name, req.Hypothesis, req.Description, req.TrafficTarget, targeting, req.MetricGroupIDs, req.PlannedStart); err != nil {
		serverError(w, r, err)
		return
	}
	if err := writeVariants(r.Context(), tx, id, req.Variants[:1]); err != nil {
		serverError(w, r, err)
		return
	}
	if err := saveTuning(r.Context(), tx, id, req.Tuning, 0, "", false); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tuning.CreateFirstRound(r.Context(), tx, id); err != nil {
		serverError(w, r, err)
		return
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "update", cur.Status, assign.StatusDraft, map[string]any{"kind": "tuning"}); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	s.writeTuning(w, r, id, http.StatusOK)
}

// PreviewTuning returns round 1's candidates for a setup, without saving.
func (s *Server) PreviewTuning(w http.ResponseWriter, r *http.Request) {
	var c tuning.Config
	if !decodeOr400(w, r, &c) {
		return
	}
	if c.Algorithm == "" {
		c.Algorithm = tuning.QuasiRandom
	}
	if err := c.Params.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	if c.Arms <= 0 {
		c.Arms = 10
	}
	if c.Arms > 20 {
		badRequest(w, "1 to 20 treatment arms")
		return
	}
	p, err := tuning.FirstRound(c, 42)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) GetTuning(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	s.writeTuning(w, r, id, http.StatusOK)
}

func (s *Server) writeTuning(w http.ResponseWriter, r *http.Request, id int64, status int) {
	t, err := s.loadTuning(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "tuning study")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	t.Experiment.Actions = availableActions(t.Experiment, user(r))
	writeJSON(w, status, t)
}

func (s *Server) loadTuning(ctx context.Context, id int64) (*Tuning, error) {
	e, err := s.loadExperiment(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if e.Kind != "tuning" {
		return nil, sql.ErrNoRows
	}
	t := &Tuning{Experiment: e, Rounds: []TuningRound{}}
	var params, base, guards, state []byte
	if err := s.DB.QueryRowContext(ctx, `
		SELECT algorithm, params, base_params, objective_metric_id, objective_direction, guardrails, arms, round_days, max_rounds,
		       min_units, keep_best, round, state, finished_at, note
		FROM tunings WHERE experiment_id = $1`, id).Scan(
		&t.Config.Algorithm, &params, &base, &t.Config.ObjectiveMetricID, &t.Config.ObjectiveDirection, &guards, &t.Config.Arms,
		&t.Config.RoundDays, &t.Config.MaxRounds, &t.Config.MinUnits, &t.Config.KeepBest, &t.Round, &state, &t.FinishedAt, &t.Note); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &t.Config.Params)
	_ = json.Unmarshal(base, &t.Config.BaseParams)
	_ = json.Unmarshal(guards, &t.Config.Guardrails)
	_ = json.Unmarshal(state, &t.State)
	if t.Config.Guardrails == nil {
		t.Config.Guardrails = []tuning.Guardrail{}
	}
	t.Objective, _ = s.metricInfo(ctx, t.Config.ObjectiveMetricID)
	t.Guardrails = []MetricInfo{}
	for _, g := range t.Config.Guardrails {
		m, _ := s.metricInfo(ctx, g.MetricID)
		m.MaxDrop = g.MaxDrop
		t.Guardrails = append(t.Guardrails, m)
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT round, status, started_at, ends_at, ended_at, arms, results, note
		FROM tuning_rounds WHERE experiment_id = $1 ORDER BY round`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var obs []tuning.Obs
	type ref struct {
		variant int64
		key     string
		round   int
	}
	var refs []ref
	for rows.Next() {
		var rd TuningRound
		var arms, results []byte
		if err := rows.Scan(&rd.Round, &rd.Status, &rd.StartedAt, &rd.EndsAt, &rd.EndedAt, &arms, &results, &rd.Note); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(arms, &rd.Arms)
		if rd.Arms == nil {
			rd.Arms = []tuning.Arm{}
		}
		if len(results) > 0 {
			rd.Results = &tuning.Results{}
			_ = json.Unmarshal(results, rd.Results)
			keys := map[int64]string{}
			for _, a := range rd.Arms {
				keys[a.VariantID] = a.Key
			}
			for _, a := range rd.Results.Arms {
				if a.Obs != nil && len(a.Obs.X) > 0 {
					obs = append(obs, *a.Obs)
					refs = append(refs, ref{a.VariantID, keys[a.VariantID], rd.Round})
				}
			}
		}
		t.Rounds = append(t.Rounds, rd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	t.Tested = len(obs)
	if b := tuning.FindBest(t.Config.Params, obs, len(t.Config.Guardrails)); b != nil {
		ref := refs[b.Obs]
		t.Best = &TuningBest{VariantID: ref.variant, Key: ref.key, Round: ref.round, Values: b.Values, Predicted: b.Predicted, Observed: b.Observed,
			Params: t.Config.Params.Apply(t.Config.BaseParams, b.Values)}
	}
	if through, ok := pipeline.DataThrough(ctx, s.DB); ok {
		t.DataThrough = &through
	}
	return t, nil
}

func (s *Server) ListTunings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{"e.kind = 'tuning'"}
	var args []any
	if p := q.Get("platform"); p != "" {
		args = append(args, p)
		where = append(where, fmt.Sprintf("(p.key = $%d OR p.id::text = $%d)", len(args), len(args)))
	}
	if b := q.Get("business"); b != "" {
		args = append(args, b)
		where = append(where, fmt.Sprintf("EXISTS (SELECT 1 FROM businesses fb WHERE fb.id = ANY(e.business_ids || e.business_id) AND (fb.key = $%d OR fb.id::text = $%d))", len(args), len(args)))
	}
	if st := q.Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where = append(where, fmt.Sprintf("e.status = ANY($%d::text[])", len(args)))
	} else {
		where = append(where, "e.status <> 'archived'")
	}
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		args = append(args, "%"+strings.ToLower(text)+"%")
		where = append(where, fmt.Sprintf("(lower(e.name) LIKE $%d OR e.id::text LIKE $%d)", len(args), len(args)))
	}
	where, args = peopleFilters(q, user(r).ID, where, args)
	page, size := 1, 25
	fmt.Sscan(q.Get("page"), &page)
	fmt.Sscan(q.Get("size"), &size)
	page, size = max(page, 1), min(max(size, 1), 100)
	cond := strings.Join(where, " AND ")
	rows, err := s.DB.QueryContext(r.Context(), experimentSelect+` WHERE `+cond+fmt.Sprintf(` ORDER BY e.updated_at DESC, e.id LIMIT %d OFFSET %d`, size, (page-1)*size), args...)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var exps []Experiment
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			rows.Close()
			serverError(w, r, err)
			return
		}
		exps = append(exps, e)
	}
	rows.Close()
	out := []TuningSummary{}
	for _, e := range exps {
		ts := TuningSummary{Experiment: e, Params: []string{}}
		var params []byte
		var objective int64
		if err := s.DB.QueryRowContext(r.Context(), `SELECT algorithm, round, max_rounds, arms, params, objective_metric_id, finished_at FROM tunings WHERE experiment_id = $1`, e.ID).
			Scan(&ts.Algorithm, &ts.Round, &ts.MaxRounds, &ts.Arms, &params, &objective, &ts.FinishedAt); err != nil {
			continue
		}
		var sp tuning.Space
		_ = json.Unmarshal(params, &sp)
		for _, p := range sp {
			ts.Params = append(ts.Params, p.Path)
		}
		if m, err := s.metricInfo(r.Context(), objective); err == nil {
			ts.Objective = m.Name
		}
		if obs, err := tuning.Observations(r.Context(), s.DB, e.ID); err == nil {
			for _, o := range obs {
				if ts.BestLift == nil || o.Y > *ts.BestLift {
					y := o.Y
					ts.BestLift = &y
				}
			}
		}
		out = append(out, ts)
	}
	var total int
	if err := s.DB.QueryRowContext(r.Context(), `
		SELECT count(*) FROM experiments e JOIN businesses b ON b.id = e.business_id JOIN platforms p ON p.id = b.platform_id
		WHERE `+cond, args...).Scan(&total); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "page": page, "size": size})
}

// AdvanceTuning ends the current round now: the pipeline catches up, the
// round is analysed and the next round's candidates start serving.
func (s *Server) AdvanceTuning(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	if _, err := pipeline.Run(r.Context(), s.DB, "tuning"); err != nil && !errors.Is(err, pipeline.ErrBusy) {
		serverError(w, r, err)
		return
	}
	actor := user(r).ID
	out, err := tuning.Advance(r.Context(), s.DB, id, time.Now().UTC(), true, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "tuning study")
		return
	}
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	s.reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

// TuningSurface is the model's expected lift over two parameters (the
// others held at the recommendation), for the search-space chart.
func (s *Server) TuningSurface(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	t, err := s.loadTuning(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "tuning study")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	var xi, yi int
	fmt.Sscan(r.URL.Query().Get("x"), &xi)
	if _, err := fmt.Sscan(r.URL.Query().Get("y"), &yi); err != nil {
		yi = -1
	}
	n := len(t.Config.Params)
	if xi < 0 || xi >= n || yi >= n || yi == xi {
		badRequest(w, "choose two different parameters")
		return
	}
	obs, err := tuning.Observations(r.Context(), s.DB, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	fixed := t.Config.Params.Controls()
	if t.Best != nil {
		fixed = t.Best.Values
	}
	grid := tuning.Surface(t.Config.Params, obs, len(t.Config.Guardrails), xi, yi, fixed, 24)
	writeJSON(w, http.StatusOK, map[string]any{"x": xi, "y": yi, "fixed": fixed, "grid": grid})
}

// TuningLoop ends rounds that are due and starts the next ones.
func (s *Server) TuningLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ids, err := tuning.Due(ctx, s.DB, time.Now().UTC())
		if err != nil {
			log.Printf("tuning: %v", err)
		}
		changed := false
		for _, id := range ids {
			out, err := tuning.Advance(ctx, s.DB, id, time.Now().UTC(), false, nil)
			if err != nil {
				if !errors.Is(err, tuning.ErrNotDue) {
					log.Printf("tuning %d: %v", id, err)
				}
				continue
			}
			changed = true
			log.Printf("tuning %d: round %d analysed (next %d, finished %v, extended %v)", id, out.Round, out.Next, out.Finished, out.Extended)
		}
		if changed {
			s.reload(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
