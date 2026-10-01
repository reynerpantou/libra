package tuning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/report"
	"github.com/reynerpantou/libra/internal/serving"
)

// Guardrail is a metric that must not get worse than MaxDrop (relative,
// 0.01 = 1%) versus v0.
type Guardrail struct {
	MetricID int64   `json:"metric_id"`
	MaxDrop  float64 `json:"max_drop"`
}

// Config is a study's setup.
type Config struct {
	Algorithm          string         `json:"algorithm"`
	Params             Space          `json:"params"`
	BaseParams         map[string]any `json:"base_params"` // everything else every arm serves (inside the platform key)
	ObjectiveMetricID  int64          `json:"objective_metric_id"`
	ObjectiveDirection string         `json:"objective_direction"` // increase | decrease
	Guardrails         []Guardrail    `json:"guardrails"`
	Arms               int            `json:"arms"`
	RoundDays          int            `json:"round_days"`
	MaxRounds          int            `json:"max_rounds"`
	MinUnits           int            `json:"min_units"` // per arm before a round may end
	KeepBest           bool           `json:"keep_best"`
}

// Arm is one variant of a round.
type Arm struct {
	VariantID int64       `json:"variant_id"`
	Key       string      `json:"key"`
	Name      string      `json:"name"`
	IsControl bool        `json:"is_control,omitempty"`
	Values    []float64   `json:"values"`
	X         []float64   `json:"x,omitempty"`
	Source    string      `json:"source"`
	Predicted *Prediction `json:"predicted,omitempty"`
}

// Estimate is one metric's result for an arm versus v0.
type Estimate struct {
	MetricID    int64   `json:"metric_id"`
	Value       float64 `json:"value"`
	RelDiff     float64 `json:"rel_diff"`
	RelCILow    float64 `json:"rel_ci_low"`
	RelCIHigh   float64 `json:"rel_ci_high"`
	PValue      float64 `json:"p_value"`
	Significant bool    `json:"significant"`
	Testable    bool    `json:"testable"`
	Verdict     string  `json:"verdict"`
	Holds       *bool   `json:"holds,omitempty"` // guardrails: within the allowed drop (by the point estimate)
}

// ArmResult is an arm's measured outcome in a round.
type ArmResult struct {
	VariantID  int64      `json:"variant_id"`
	Units      int64      `json:"units"`
	Objective  *Estimate  `json:"objective,omitempty"`
	Guardrails []Estimate `json:"guardrails"`
	Obs        *Obs       `json:"obs,omitempty"` // what the algorithm learns from it
}

// Results are a round's measured outcome.
type Results struct {
	From         time.Time   `json:"from"`
	To           time.Time   `json:"to"`
	ControlUnits int64       `json:"control_units"`
	ControlValue float64     `json:"control_value"`
	Arms         []ArmResult `json:"arms"`
	ComputedAt   time.Time   `json:"computed_at"`
}

// RoundEnd is when a round that starts at start ends. Rounds end at 00:00
// UTC so daily metrics never mix two rounds: a round lasts `days` days,
// rounded to the nearest midnight (a round started mid-afternoon runs a
// little longer, one started in the morning a little shorter).
func RoundEnd(start time.Time, days int) time.Time {
	t := start.UTC().Add(time.Duration(days)*24*time.Hour - 12*time.Hour)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(t) {
		day = day.AddDate(0, 0, 1)
	}
	return day
}

// Weights splits traffic evenly; v0 takes the remainder.
func Weights(arms int) (control, arm int) {
	arm = 1000 / (arms + 1)
	return 1000 - arm*arms, arm
}

// ArmParams is the full params of an arm: base with the tuned values, under
// the platform key.
func ArmParams(platformKey string, cfg Config, values []float64) map[string]any {
	return map[string]any{platformKey: cfg.Params.Apply(cfg.BaseParams, values)}
}

// FirstRound proposes round 1.
func FirstRound(cfg Config, seed int64) (Proposal, error) {
	return Propose(Request{Algorithm: cfg.Algorithm, Space: cfg.Params, Arms: cfg.Arms, Seed: seed, Round: 1,
		KeepBest: cfg.KeepBest, Guardrails: len(cfg.Guardrails)})
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// study is a study's stored row.
type study struct {
	cfg         Config
	expID       int64
	status      string
	platformKey string
	seed        int64
	baseSalt    string
	round       int
	state       State
	finished    bool
}

func loadStudy(ctx context.Context, q execer, expID int64, lock bool) (*study, error) {
	s := &study{expID: expID}
	var params, base, guards, state []byte
	var finished sql.NullTime
	sfx := ""
	if lock {
		sfx = " FOR UPDATE OF t"
	}
	err := q.QueryRowContext(ctx, `
		SELECT t.algorithm, t.params, t.base_params, t.objective_metric_id, t.objective_direction, t.guardrails, t.arms,
		       t.round_days, t.max_rounds, t.min_units, t.keep_best, t.seed, t.base_salt, t.round, t.state, t.finished_at,
		       e.status, p.key
		FROM tunings t JOIN experiments e ON e.id = t.experiment_id
		JOIN businesses b ON b.id = e.business_id JOIN platforms p ON p.id = b.platform_id
		WHERE t.experiment_id = $1`+sfx, expID).Scan(
		&s.cfg.Algorithm, &params, &base, &s.cfg.ObjectiveMetricID, &s.cfg.ObjectiveDirection, &guards, &s.cfg.Arms,
		&s.cfg.RoundDays, &s.cfg.MaxRounds, &s.cfg.MinUnits, &s.cfg.KeepBest, &s.seed, &s.baseSalt, &s.round, &state, &finished,
		&s.status, &s.platformKey)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &s.cfg.Params)
	_ = json.Unmarshal(base, &s.cfg.BaseParams)
	_ = json.Unmarshal(guards, &s.cfg.Guardrails)
	_ = json.Unmarshal(state, &s.state)
	if s.cfg.BaseParams == nil {
		s.cfg.BaseParams = map[string]any{}
	}
	s.finished = finished.Valid
	return s, nil
}

// StartRound starts the current round serving (when the experiment starts).
func StartRound(ctx context.Context, tx execer, expID int64, now time.Time) error {
	var days int
	if err := tx.QueryRowContext(ctx, `SELECT round_days FROM tunings WHERE experiment_id = $1`, expID).Scan(&days); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE tuning_rounds SET status = 'running', started_at = $2, ends_at = $3
		WHERE experiment_id = $1 AND status = 'planned' AND round = (SELECT round FROM tunings WHERE experiment_id = $1)`,
		expID, now, RoundEnd(now, days))
	return err
}

// Finish marks a study finished (it was stopped, launched or archived).
func Finish(ctx context.Context, tx execer, expID int64, now time.Time, note string) error {
	_, err := tx.ExecContext(ctx, `UPDATE tunings SET finished_at = COALESCE(finished_at, $2), note = CASE WHEN finished_at IS NULL THEN $3 ELSE note END WHERE experiment_id = $1`, expID, now, note)
	return err
}

// ErrNotDue means the current round can't end yet.
var ErrNotDue = errors.New("the round isn't due yet")

// Outcome says what Advance did.
type Outcome struct {
	Round    int    `json:"round"`    // the round that was analysed
	Next     int    `json:"next"`     // the round now running (0 when finished)
	Finished bool   `json:"finished"` // the study ended
	Extended bool   `json:"extended"` // the round was extended for lack of units
	Note     string `json:"note"`
}

// Advance ends the current round at `at`, analyses it, and either starts the
// next round with new candidates or finishes the study. Unless force is
// set, the round must be due and have enough units per arm (otherwise it's
// extended by a day).
func Advance(ctx context.Context, db *sql.DB, expID int64, at time.Time, force bool, actor *int64) (Outcome, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback()
	s, err := loadStudy(ctx, tx, expID, true)
	if err != nil {
		return Outcome{}, err
	}
	if s.finished {
		return Outcome{}, errors.New("the study has finished")
	}
	if s.status != "active" && s.status != "paused" {
		return Outcome{}, fmt.Errorf("the study isn't running (it's %s)", s.status)
	}
	var started, endsAt sql.NullTime
	var armsJSON []byte
	var rstatus string
	if err := tx.QueryRowContext(ctx, `SELECT started_at, ends_at, arms, status FROM tuning_rounds WHERE experiment_id = $1 AND round = $2`, expID, s.round).
		Scan(&started, &endsAt, &armsJSON, &rstatus); err != nil {
		return Outcome{}, err
	}
	if rstatus != "running" || !started.Valid {
		return Outcome{}, errors.New("the current round hasn't started")
	}
	if !force {
		if !endsAt.Valid || at.Before(endsAt.Time) {
			return Outcome{}, ErrNotDue
		}
		// A scheduled end is exact, however late the loop runs: the next
		// round starts at the same midnight.
		at = endsAt.Time
	}
	if !at.After(started.Time) {
		return Outcome{}, errors.New("the round has no time to analyse yet")
	}
	var arms []Arm
	_ = json.Unmarshal(armsJSON, &arms)

	res, err := measure(ctx, db, s, arms, started.Time, at)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Round: s.round}
	if !force && s.cfg.MinUnits > 0 {
		short := false
		for _, a := range res.Arms {
			if a.Units < int64(s.cfg.MinUnits) {
				short = true
			}
		}
		if short || res.ControlUnits < int64(s.cfg.MinUnits) {
			next := RoundEnd(at, 1)
			if _, err := tx.ExecContext(ctx, `UPDATE tuning_rounds SET ends_at = $3, note = $4 WHERE experiment_id = $1 AND round = $2`,
				expID, s.round, next, fmt.Sprintf("Extended: an arm had fewer than %d units.", s.cfg.MinUnits)); err != nil {
				return Outcome{}, err
			}
			out.Extended, out.Next, out.Note = true, s.round, "extended a day: not enough units yet"
			return out, tx.Commit()
		}
	}
	resJSON, _ := json.Marshal(res)
	if _, err := tx.ExecContext(ctx, `UPDATE tuning_rounds SET status = 'analyzed', ended_at = $3, results = $4 WHERE experiment_id = $1 AND round = $2`,
		expID, s.round, at, resJSON); err != nil {
		return Outcome{}, err
	}
	obs, err := observations(ctx, tx, expID)
	if err != nil {
		return Outcome{}, err
	}

	if s.round >= s.cfg.MaxRounds {
		best := FindBest(s.cfg.Params, obs, len(s.cfg.Guardrails))
		note := "Finished all rounds."
		if best != nil {
			note = fmt.Sprintf("Finished all %d rounds. Best: %s (expected %+.2f%%).", s.cfg.MaxRounds, describe(s.cfg.Params, best.Values), best.Predicted.Mean*100)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tunings SET finished_at = $2, note = $3 WHERE experiment_id = $1`, expID, at, note); err != nil {
			return Outcome{}, err
		}
		// Stop serving the arms; launching the best arm is a separate,
		// reviewed decision.
		if _, err := tx.ExecContext(ctx, `UPDATE experiments SET status = 'stopped', buckets = '{}', ended_at = COALESCE(ended_at, $2), updated_at = now() WHERE id = $1`, expID, at); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rollouts SET status = 'cancelled', note = 'the tuning study finished', updated_at = now() WHERE experiment_id = $1 AND status = 'active'`, expID); err != nil {
			return Outcome{}, err
		}
		if err := auditTx(ctx, tx, actor, expID, "tuning_finished", s.status, "stopped", map[string]any{"round": s.round, "note": note}); err != nil {
			return Outcome{}, err
		}
		out.Finished, out.Note = true, note
	} else {
		next := s.round + 1
		p, err := Propose(Request{Algorithm: s.cfg.Algorithm, Space: s.cfg.Params, Arms: s.cfg.Arms, Seed: s.seed, Round: next,
			KeepBest: s.cfg.KeepBest, Guardrails: len(s.cfg.Guardrails), Obs: obs, State: s.state})
		if err != nil {
			return Outcome{}, err
		}
		newArms, err := createArms(ctx, tx, s, next, p.Candidates)
		if err != nil {
			return Outcome{}, err
		}
		// The control keeps its id; it joins every round.
		for _, a := range arms {
			if a.IsControl {
				newArms = append([]Arm{a}, newArms...)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE variants SET weight = 0, retired = true WHERE experiment_id = $1 AND NOT is_control AND round = $2`, expID, s.round); err != nil {
			return Outcome{}, err
		}
		stateJSON, _ := json.Marshal(p.State)
		if _, err := tx.ExecContext(ctx, `UPDATE tunings SET round = $2, state = $3 WHERE experiment_id = $1`, expID, next, stateJSON); err != nil {
			return Outcome{}, err
		}
		// A new salt re-randomises units across the arms each round, so
		// what a unit saw last round doesn't line up with its new arm.
		if _, err := tx.ExecContext(ctx, `UPDATE experiments SET salt = $2, updated_at = now() WHERE id = $1`, expID, fmt.Sprintf("%s:r%d", s.baseSalt, next)); err != nil {
			return Outcome{}, err
		}
		armsOut, _ := json.Marshal(newArms)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tuning_rounds (experiment_id, round, started_at, ends_at, status, arms, note) VALUES ($1, $2, $3, $4, 'running', $5, $6)`,
			expID, next, at, RoundEnd(at, s.cfg.RoundDays), armsOut, p.Note); err != nil {
			return Outcome{}, err
		}
		if err := auditTx(ctx, tx, actor, expID, "tuning_round", "", "", map[string]any{"analysed": s.round, "started": next}); err != nil {
			return Outcome{}, err
		}
		out.Next, out.Note = next, p.Note
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return Outcome{}, err
	}
	return out, tx.Commit()
}

// createArms inserts the variants for a round's candidates.
func createArms(ctx context.Context, tx execer, s *study, round int, cands []Candidate) ([]Arm, error) {
	_, w := Weights(s.cfg.Arms)
	var out []Arm
	for i, c := range cands {
		key := fmt.Sprintf("r%d_v%d", round, i+1)
		name := fmt.Sprintf("v%d", i+1)
		params, _ := json.Marshal(ArmParams(s.platformKey, s.cfg, c.Values))
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position, round)
			VALUES ($1, $2, $3, false, $4, $5, $6, $7) RETURNING id`,
			s.expID, key, name, w, params, i+1, round).Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, Arm{VariantID: id, Key: key, Name: name, Values: c.Values, X: c.X, Source: c.Source, Predicted: c.Predicted})
	}
	return out, nil
}

// CreateFirstRound writes round 1's arms for a new (draft) study whose
// control variant already exists, replacing any earlier plan.
func CreateFirstRound(ctx context.Context, tx execer, expID int64) error {
	s, err := loadStudy(ctx, tx, expID, false)
	if err != nil {
		return err
	}
	p, err := FirstRound(s.cfg, s.seed)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM variants WHERE experiment_id = $1 AND NOT is_control`, expID); err != nil {
		return err
	}
	arms, err := createArms(ctx, tx, s, 1, p.Candidates)
	if err != nil {
		return err
	}
	var ctl Arm
	if err := tx.QueryRowContext(ctx, `SELECT id, key, name FROM variants WHERE experiment_id = $1 AND is_control`, expID).Scan(&ctl.VariantID, &ctl.Key, &ctl.Name); err != nil {
		return err
	}
	ctl.IsControl, ctl.Values, ctl.Source = true, s.cfg.Params.Controls(), "control"
	arms = append([]Arm{ctl}, arms...)
	armsJSON, _ := json.Marshal(arms)
	stateJSON, _ := json.Marshal(p.State)
	if _, err := tx.ExecContext(ctx, `UPDATE tunings SET round = 1, state = $2 WHERE experiment_id = $1`, expID, stateJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tuning_rounds WHERE experiment_id = $1`, expID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tuning_rounds (experiment_id, round, status, arms, note) VALUES ($1, 1, 'planned', $2, $3)`, expID, armsJSON, p.Note)
	return err
}

// measure computes a round's results.
func measure(ctx context.Context, db *sql.DB, s *study, arms []Arm, from, to time.Time) (*Results, error) {
	var vs []report.Variant
	for _, a := range arms {
		vs = append(vs, report.Variant{ID: a.VariantID, Key: a.Key, Name: a.Name, IsControl: a.IsControl})
	}
	metricIDs := []int64{s.cfg.ObjectiveMetricID}
	for _, g := range s.cfg.Guardrails {
		metricIDs = append(metricIDs, g.MetricID)
	}
	seg, err := report.ArmResults(ctx, db, s.expID, vs, from, to, metricIDs, 0.05)
	if err != nil {
		return nil, err
	}
	byMetric := map[int64]report.MetricResult{}
	for _, m := range seg.Metrics {
		byMetric[m.MetricID] = m
	}
	res := &Results{From: from, To: to, ComputedAt: time.Now().UTC(), Arms: []ArmResult{}}
	units := map[int64]int64{}
	for _, v := range vs {
		units[v.ID] = v.Units
		if v.IsControl {
			res.ControlUnits = v.Units
			if m, ok := byMetric[s.cfg.ObjectiveMetricID]; ok {
				for _, val := range m.Values {
					if val.VariantID == v.ID {
						res.ControlValue = finite(float64(val.Value))
					}
				}
			}
		}
	}
	const z = 1.959963984540054
	for _, a := range arms {
		if a.IsControl {
			continue
		}
		ar := ArmResult{VariantID: a.VariantID, Units: units[a.VariantID], Guardrails: []Estimate{}}
		est := func(id int64) *Estimate {
			m, ok := byMetric[id]
			if !ok {
				return nil
			}
			e := &Estimate{MetricID: id}
			for _, v := range m.Values {
				if v.VariantID == a.VariantID {
					e.Value = finite(float64(v.Value))
				}
			}
			for _, c := range m.Comparisons {
				if c.VariantID == a.VariantID {
					e.RelDiff, e.RelCILow, e.RelCIHigh = finite(float64(c.RelDiff)), finite(float64(c.RelCILow)), finite(float64(c.RelCIHigh))
					e.PValue, e.Significant, e.Testable, e.Verdict = finite(float64(c.PValue)), c.Significant, c.Testable, c.Verdict
				}
			}
			return e
		}
		ar.Objective = est(s.cfg.ObjectiveMetricID)
		ok := ar.Objective != nil && ar.Objective.Testable && ar.Objective.RelCIHigh > ar.Objective.RelCILow
		var o Obs
		if ok {
			se := (ar.Objective.RelCIHigh - ar.Objective.RelCILow) / (2 * z)
			y := ar.Objective.RelDiff
			if s.cfg.ObjectiveDirection == "decrease" {
				y = -y
			}
			o = Obs{X: a.X, Y: y, Var: se * se, Round: s.round}
		}
		for _, g := range s.cfg.Guardrails {
			e := est(g.MetricID)
			if e == nil {
				e = &Estimate{MetricID: g.MetricID}
			}
			dir := metricDirection(ctx, db, g.MetricID)
			lift := e.RelDiff
			switch dir {
			case "decrease":
				lift = -lift
			case "neutral":
				lift = -math.Abs(lift)
			}
			holds := lift+g.MaxDrop >= 0
			e.Holds = &holds
			ar.Guardrails = append(ar.Guardrails, *e)
			if ok {
				se := 0.0
				if e.Testable && e.RelCIHigh > e.RelCILow {
					se = (e.RelCIHigh - e.RelCILow) / (2 * z)
				} else {
					se = 1 // unknown: the model shouldn't trust it
				}
				o.Cons = append(o.Cons, Measure{Y: lift + g.MaxDrop, Var: se * se})
			}
		}
		if ok {
			ar.Obs = &o
		}
		res.Arms = append(res.Arms, ar)
	}
	return res, nil
}

func metricDirection(ctx context.Context, db *sql.DB, id int64) string {
	var d string
	_ = db.QueryRowContext(ctx, `SELECT direction FROM metrics WHERE id = $1`, id).Scan(&d)
	return d
}

// observations collects what every analysed round learned.
func observations(ctx context.Context, q execer, expID int64) ([]Obs, error) {
	rows, err := q.QueryContext(ctx, `SELECT results FROM tuning_rounds WHERE experiment_id = $1 AND status = 'analyzed' AND results IS NOT NULL ORDER BY round`, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Obs
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Results
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		for _, a := range r.Arms {
			if a.Obs != nil && len(a.Obs.X) > 0 {
				out = append(out, *a.Obs)
			}
		}
	}
	return out, rows.Err()
}

// Observations is exported for the API (best point, charts).
func Observations(ctx context.Context, db *sql.DB, expID int64) ([]Obs, error) {
	return observations(ctx, db, expID)
}

// Due lists running studies whose current round has ended and whose data
// the pipeline has processed.
func Due(ctx context.Context, db *sql.DB, now time.Time) ([]int64, error) {
	through, ok := pipeline.DataThrough(ctx, db)
	if !ok {
		return nil, nil
	}
	if through.After(now) {
		through = now
	}
	rows, err := db.QueryContext(ctx, `
		SELECT r.experiment_id FROM tuning_rounds r
		JOIN tunings t ON t.experiment_id = r.experiment_id AND t.round = r.round AND t.finished_at IS NULL
		JOIN experiments e ON e.id = r.experiment_id AND e.status = 'active'
		WHERE r.status = 'running' AND r.ends_at <= $1`, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RoundEndsAt is when the current round of a study is due to end.
func RoundEndsAt(ctx context.Context, db *sql.DB, expID int64) (time.Time, error) {
	var t sql.NullTime
	err := db.QueryRowContext(ctx, `SELECT r.ends_at FROM tuning_rounds r JOIN tunings t ON t.experiment_id = r.experiment_id AND t.round = r.round WHERE r.experiment_id = $1`, expID).Scan(&t)
	if !t.Valid {
		return time.Time{}, err
	}
	return t.Time, err
}

func auditTx(ctx context.Context, tx execer, actor *int64, expID int64, action, from, to string, detail any) error {
	d, _ := json.Marshal(detail)
	var f, t any
	if from != "" {
		f = from
	}
	if to != "" {
		t = to
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (experiment_id, entity, entity_id, actor_id, action, from_status, to_status, detail)
		VALUES ($1, 'experiment', $1, $2, $3, $4, $5, $6)`, expID, actor, action, f, t, d)
	return err
}

func describe(sp Space, vals []float64) string {
	out := ""
	for i, p := range sp {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s = %g", p.Path, vals[i])
	}
	return out
}

func finite(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}
