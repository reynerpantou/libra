package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/serving"
)

// Traffic and launches change either immediately or gradually. A gradual
// change is a rollout plan: the value moves up by a step every interval
// until it reaches the target. Traffic plans ramp a running experiment's
// share of its layer; launch plans ramp the share of units a launched
// variant serves (the rest keep the previous defaults). Raising either
// keeps every unit already in, so a ramp never reshuffles anyone.

// Gradual asks for a stepped change instead of an immediate one.
type Gradual struct {
	Start           int `json:"start"` // per mille to begin at (launches and starts); 0 = one step
	Step            int `json:"step"`  // per mille added each interval
	IntervalMinutes int `json:"interval_minutes"`
}

func (g *Gradual) check(from, target int) string {
	if g == nil {
		return ""
	}
	if g.Step < 1 || g.Step > assign.Buckets {
		return "the step must be between 0.1% and 100%"
	}
	if g.IntervalMinutes < 1 || g.IntervalMinutes > 7*24*60 {
		return "the interval must be between 1 minute and 7 days"
	}
	if target <= from {
		return "a gradual rollout only goes up; lower it immediately instead"
	}
	if g.Start < 0 || g.Start > target {
		return "the starting share must be between 0% and the target"
	}
	return ""
}

// first is the value a gradual change applies right away.
func (g *Gradual) first(from, target int) int {
	v := from + g.Step
	if g.Start > from {
		v = g.Start
	}
	return min(v, target)
}

type Rollout struct {
	ID           int64     `json:"id"`
	ExperimentID int64     `json:"experiment_id"`
	Kind         string    `json:"kind"` // traffic | launch
	StartValue   int       `json:"start_value"`
	Target       int       `json:"target"`
	Step         int       `json:"step"`
	IntervalSecs int       `json:"interval_secs"`
	NextAt       time.Time `json:"next_at"`
	Status       string    `json:"status"` // active | done | cancelled | blocked
	Note         string    `json:"note"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type userErr string

func (e userErr) Error() string { return string(e) }

// applyTraffic sets an experiment's traffic target; a running experiment
// re-allocates its buckets now. It returns the new status (an approved
// experiment goes back to draft: traffic is part of what was reviewed).
func applyTraffic(ctx context.Context, tx *sql.Tx, s *Server, e Experiment, target int) error {
	switch e.Status {
	case assign.StatusStopped, assign.StatusLaunched, assign.StatusArchived:
		return userErr("a finished experiment's traffic can't change")
	case assign.StatusInReview:
		return userErr("withdraw the review before changing traffic")
	}
	sets := "traffic_target = $2, updated_at = now()"
	args := []any{e.ID, target}
	if e.Status == assign.StatusActive || e.Status == assign.StatusPaused {
		taken, err := takenBuckets(ctx, tx, e.LayerID, e.ID)
		if err != nil {
			return err
		}
		cur, salt, err := currentBuckets(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		next, err := assign.Allocate(cur, target, taken, salt)
		if err != nil {
			return userErr(err.Error())
		}
		sets += ", buckets = $3"
		args = append(args, next)
	} else {
		free, err := s.layerFree(ctx, tx, e.LayerID, e.ID)
		if err != nil {
			return err
		}
		if target > free {
			return userErr(fmt.Sprintf("the layer only has %s free", pct(free)))
		}
		if e.Status == assign.StatusApproved {
			sets += ", status = 'draft'"
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE experiments SET `+sets+` WHERE id = $1`, args...)
	return err
}

// startPlan replaces any active plan of the kind with a new one whose first
// step has already been applied (at value).
func startPlan(ctx context.Context, tx *sql.Tx, expID int64, kind string, from, value, target int, g *Gradual, actor int64) error {
	if err := cancelPlans(ctx, tx, expID, kind, "replaced by a new change"); err != nil {
		return err
	}
	status, note := "active", ""
	if value >= target {
		status, note = "done", "reached the target"
	}
	var by any
	if actor > 0 {
		by = actor
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO rollouts (experiment_id, kind, start_value, target, step, interval_secs, next_at, status, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6::int, now() + make_interval(secs => $6::int), $7, $8, $9)`,
		expID, kind, from, target, g.Step, g.IntervalMinutes*60, status, note, by)
	return err
}

func cancelPlans(ctx context.Context, tx *sql.Tx, expID int64, kind, note string) error {
	q := `UPDATE rollouts SET status = 'cancelled', note = $2, updated_at = now() WHERE experiment_id = $1 AND status IN ('active', 'blocked')`
	args := []any{expID, note}
	if kind != "" {
		q += ` AND kind = $3`
		args = append(args, kind)
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// SetTraffic changes a running (or not yet started) experiment's traffic,
// or a launched experiment's rollout share — immediately, or gradually
// with a plan.
func (s *Server) SetTraffic(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		TrafficTarget int      `json:"traffic_target"`
		Gradual       *Gradual `json:"gradual,omitempty"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	if req.TrafficTarget < 0 || req.TrafficTarget > assign.Buckets {
		badRequest(w, "traffic must be between 0% and 100%")
		return
	}
	e, err := s.loadExperiment(r.Context(), id, false)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	launch := e.Status == assign.StatusLaunched
	from := e.TrafficTarget
	kind := "traffic"
	if launch {
		from, kind = e.LaunchRollout, "launch"
		if req.TrafficTarget < 1 {
			badRequest(w, "a launch serves at least 0.1% of units; to stop serving it, archive the experiment")
			return
		}
	}
	if req.Gradual != nil {
		if msg := req.Gradual.check(from, req.TrafficTarget); msg != "" {
			badRequest(w, msg)
			return
		}
		if !launch && e.Status != assign.StatusActive && e.Status != assign.StatusPaused {
			badRequest(w, "a gradual ramp runs while the experiment runs — start it first, or start it gradually")
			return
		}
	}
	value := req.TrafficTarget
	if req.Gradual != nil {
		value = req.Gradual.first(from, req.TrafficTarget)
	}

	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if launch {
		_, err = tx.ExecContext(r.Context(), `UPDATE experiments SET launch_rollout = $2, updated_at = now() WHERE id = $1`, id, value)
	} else {
		err = applyTraffic(r.Context(), tx, s, e, value)
	}
	var ue userErr
	if errors.As(err, &ue) {
		badRequest(w, string(ue))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if req.Gradual != nil {
		err = startPlan(r.Context(), tx, id, kind, from, value, req.TrafficTarget, req.Gradual, user(r).ID)
	} else {
		err = cancelPlans(r.Context(), tx, id, kind, "replaced by an immediate change")
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	detail := map[string]any{"from": pct(from), "to": pct(value)}
	if req.Gradual != nil {
		detail["target"] = pct(req.TrafficTarget)
		detail["gradual"] = fmt.Sprintf("+%s every %d min", pct(req.Gradual.Step), req.Gradual.IntervalMinutes)
	}
	action := "traffic"
	if launch {
		action = "launch_rollout"
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, action, "", "", detail); err != nil {
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
	out, _ := s.loadExperiment(r.Context(), id, true)
	out.Actions = availableActions(out, user(r))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListRollouts(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT ro.id, ro.experiment_id, ro.kind, ro.start_value, ro.target, ro.step, ro.interval_secs, ro.next_at, ro.status, ro.note,
		       COALESCE(NULLIF(u.display_name, ''), u.username, 'Libra'), ro.created_at, ro.updated_at
		FROM rollouts ro LEFT JOIN users u ON u.id = ro.created_by
		WHERE ro.experiment_id = $1 ORDER BY ro.id DESC LIMIT 20`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Rollout{}
	for rows.Next() {
		var x Rollout
		if err := rows.Scan(&x.ID, &x.ExperimentID, &x.Kind, &x.StartValue, &x.Target, &x.Step, &x.IntervalSecs, &x.NextAt, &x.Status, &x.Note,
			&x.CreatedBy, &x.CreatedAt, &x.UpdatedAt); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// CancelRollout stops a plan where it is; the current value stays.
func (s *Server) CancelRollout(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	rid, _ := pathID(r, "rid")
	res, err := s.DB.ExecContext(r.Context(), `
		UPDATE rollouts SET status = 'cancelled',
		       note = 'cancelled by ' || COALESCE((SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = $3), 'someone'), updated_at = now()
		WHERE id = $1 AND experiment_id = $2 AND status IN ('active', 'blocked')`, rid, id, user(r).ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "active rollout")
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, id, "experiment", id, "rollout_cancel", "", "", map[string]any{"rollout": rid})
	w.WriteHeader(http.StatusNoContent)
}

// RunRollouts applies every due step. It's safe to run from several
// processes: each plan is locked while it steps.
func (s *Server) RunRollouts(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM rollouts WHERE status = 'active' AND next_at <= now() ORDER BY next_at LIMIT 100`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		changed, err := s.stepRollout(ctx, id)
		if err != nil {
			return n, fmt.Errorf("rollout %d: %w", id, err)
		}
		if changed {
			n++
		}
	}
	if n > 0 {
		s.reload(ctx)
	}
	return n, nil
}

func (s *Server) stepRollout(ctx context.Context, rid int64) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var ro Rollout
	err = tx.QueryRowContext(ctx, `
		SELECT id, experiment_id, kind, target, step, interval_secs FROM rollouts
		WHERE id = $1 AND status = 'active' AND next_at <= now() FOR UPDATE SKIP LOCKED`, rid).
		Scan(&ro.ID, &ro.ExperimentID, &ro.Kind, &ro.Target, &ro.Step, &ro.IntervalSecs)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(status, note string) (bool, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE rollouts SET status = $2, note = $3, updated_at = now() WHERE id = $1`, rid, status, note); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	later := func() (bool, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE rollouts SET next_at = now() + make_interval(secs => $2), updated_at = now() WHERE id = $1`, rid, ro.IntervalSecs); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	e, err := s.loadExperiment(ctx, ro.ExperimentID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return finish("cancelled", "the experiment is gone")
	}
	if err != nil {
		return false, err
	}
	var from, next int
	switch ro.Kind {
	case "traffic":
		if e.Status == assign.StatusPaused {
			return later() // resume stepping once it runs again
		}
		if e.Status != assign.StatusActive {
			return finish("cancelled", "the experiment is "+e.Status)
		}
		from = e.TrafficTarget
		next = min(from+ro.Step, ro.Target)
		if from >= ro.Target {
			return finish("done", "reached the target")
		}
		// Take what the layer can give; a full layer blocks the plan.
		free, err := s.layerFree(ctx, tx, e.LayerID, e.ID)
		if err != nil {
			return false, err
		}
		if next > free {
			next = free
		}
		if next <= from {
			return finish("blocked", fmt.Sprintf("the layer is full at %s; free traffic and ramp again", pct(from)))
		}
		if err := applyTraffic(ctx, tx, s, e, next); err != nil {
			var ue userErr
			if errors.As(err, &ue) {
				return finish("blocked", string(ue))
			}
			return false, err
		}
	case "launch":
		if e.Status != assign.StatusLaunched {
			return finish("cancelled", "the experiment is "+e.Status)
		}
		from = e.LaunchRollout
		if from >= ro.Target {
			return finish("done", "reached the target")
		}
		next = min(from+ro.Step, ro.Target)
		if _, err := tx.ExecContext(ctx, `UPDATE experiments SET launch_rollout = $2, updated_at = now() WHERE id = $1`, e.ID, next); err != nil {
			return false, err
		}
	}
	status, note := "active", fmt.Sprintf("at %s", pct(next))
	if next >= ro.Target {
		status, note = "done", "reached the target"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE rollouts SET status = $2, note = $3, next_at = now() + make_interval(secs => $4), updated_at = now() WHERE id = $1`,
		rid, status, note, ro.IntervalSecs); err != nil {
		return false, err
	}
	action := "traffic"
	if ro.Kind == "launch" {
		action = "launch_rollout"
	}
	if err := audit(ctx, tx, 0, e.ID, "experiment", e.ID, action, "", "", map[string]any{
		"from": pct(from), "to": pct(next), "target": pct(ro.Target), "gradual": "scheduled step",
	}); err != nil {
		return false, err
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// RolloutLoop steps due rollouts every interval until ctx ends.
func (s *Server) RolloutLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.RunRollouts(ctx); err != nil {
			log.Printf("rollouts: %v", err)
		} else if n > 0 {
			log.Printf("rollouts: %d step(s) applied", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
