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
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/tuning"
)

// ---- Extend ----

// ExtendExperiment moves an experiment's end date later (or clears it), or
// adds rounds to a tuning study.
func (s *Server) ExtendExperiment(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		EndAt  *time.Time `json:"end_at"` // AB tests: the new end (null: no end date)
		Rounds int        `json:"rounds"` // tuning: rounds to add
	}
	if !decodeOr400(w, r, &req) {
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
	switch e.Status {
	case assign.StatusStopped, assign.StatusLaunched, assign.StatusArchived:
		badRequest(w, "it has finished; there's nothing to extend")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	detail := map[string]any{}
	if e.Kind == "tuning" {
		if req.Rounds < 1 || req.Rounds > 50 {
			badRequest(w, "add 1 to 50 rounds")
			return
		}
		var max int
		if err := tx.QueryRowContext(r.Context(), `UPDATE tunings SET max_rounds = max_rounds + $2 WHERE experiment_id = $1 AND finished_at IS NULL RETURNING max_rounds`, id, req.Rounds).Scan(&max); err != nil {
			badRequest(w, "the study has finished")
			return
		}
		if max > 100 {
			badRequest(w, "a study has at most 100 rounds")
			return
		}
		detail["rounds"], detail["max_rounds"] = req.Rounds, max
	} else {
		if req.EndAt != nil && !req.EndAt.After(time.Now()) {
			badRequest(w, "the new end must be in the future")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE experiments SET end_at = $2, updated_at = now() WHERE id = $1`, id, req.EndAt); err != nil {
			serverError(w, r, err)
			return
		}
		if req.EndAt != nil {
			detail["end_at"] = req.EndAt.UTC().Format(time.RFC3339)
		} else {
			detail["end_at"] = "none"
		}
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "extend", "", "", detail); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	out, _ := s.loadExperiment(r.Context(), id, true)
	out.Actions = availableActions(out, user(r))
	writeJSON(w, http.StatusOK, out)
}

// ---- Notifications ----

type Notification struct {
	ID             int64          `json:"id"`
	Kind           string         `json:"kind"`
	ExperimentID   *int64         `json:"experiment_id"`
	Experiment     string         `json:"experiment"`
	ExperimentKind string         `json:"experiment_kind"`
	Status         string         `json:"status"`
	Actor          string         `json:"actor"`
	Detail         map[string]any `json:"detail"`
	CreatedAt      time.Time      `json:"created_at"`
	Read           bool           `json:"read"`
}

func (s *Server) ListNotifications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	me := user(r).ID
	limit := 20
	if q.Get("limit") == "50" {
		limit = 50
	}
	var before int64
	if b := q.Get("before"); b != "" {
		before, _ = strconv.ParseInt(b, 10, 64)
	}
	cond := "n.user_id = $1"
	args := []any{me}
	if before > 0 {
		args = append(args, before)
		cond += " AND n.id < $2"
	}
	if q.Get("unread") == "1" {
		cond += " AND n.read_at IS NULL"
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT n.id, n.kind, n.experiment_id, COALESCE(e.name, ''), COALESCE(e.kind, ''), COALESCE(e.status, ''),
		       COALESCE(NULLIF(a.display_name, ''), a.username, ''), n.detail, n.created_at, n.read_at IS NOT NULL
		FROM notifications n
		LEFT JOIN experiments e ON e.id = n.experiment_id
		LEFT JOIN users a ON a.id = n.actor_id
		WHERE `+cond+` ORDER BY n.id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var detail []byte
		if err := rows.Scan(&n.ID, &n.Kind, &n.ExperimentID, &n.Experiment, &n.ExperimentKind, &n.Status, &n.Actor, &detail, &n.CreatedAt, &n.Read); err != nil {
			serverError(w, r, err)
			return
		}
		_ = json.Unmarshal(detail, &n.Detail)
		out = append(out, n)
	}
	var unread int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, me).Scan(&unread)
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "unread": unread})
}

func (s *Server) UnreadNotifications(w http.ResponseWriter, r *http.Request) {
	var unread int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, user(r).ID).Scan(&unread); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": unread})
}

func (s *Server) ReadNotifications(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	var err error
	if req.All {
		_, err = s.DB.ExecContext(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, user(r).ID)
	} else if len(req.IDs) > 0 {
		_, err = s.DB.ExecContext(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2::bigint[]) AND read_at IS NULL`, user(r).ID, req.IDs)
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Schedule loop ----

// RunSchedule sends reminders and ends experiments that reached their end
// date. It returns how many experiments it ended.
func (s *Server) RunSchedule(ctx context.Context) (int, error) {
	// Time to start: the planned start has passed and it hasn't started.
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO notifications (user_id, experiment_id, kind, detail, dedupe)
		SELECT e.owner_id, e.id, 'start_due', jsonb_build_object('status', e.status, 'planned_start', e.planned_start),
		       'start_due:' || e.id || ':' || extract(epoch FROM e.planned_start)::bigint
		FROM experiments e
		WHERE e.owner_id IS NOT NULL AND e.planned_start <= now() AND e.started_at IS NULL
		  AND e.status IN ('draft', 'in_review', 'approved', 'rejected')
		ON CONFLICT (user_id, dedupe) WHERE dedupe IS NOT NULL DO NOTHING`); err != nil {
		return 0, err
	}
	// Ending within a day: AB tests by end date, tuning studies in their
	// last round.
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO notifications (user_id, experiment_id, kind, detail, dedupe)
		SELECT e.owner_id, e.id, 'ending_soon', jsonb_build_object('end_at', e.end_at),
		       'ending_soon:' || e.id || ':' || extract(epoch FROM e.end_at)::bigint
		FROM experiments e
		WHERE e.owner_id IS NOT NULL AND e.kind = 'ab' AND e.status IN ('active', 'paused')
		  AND e.end_at > now() AND e.end_at <= now() + interval '1 day'
		UNION ALL
		SELECT e.owner_id, e.id, 'ending_soon', jsonb_build_object('end_at', r.ends_at, 'round', t.round),
		       'ending_soon:' || e.id || ':' || extract(epoch FROM r.ends_at)::bigint
		FROM experiments e
		JOIN tunings t ON t.experiment_id = e.id AND t.finished_at IS NULL AND t.round >= t.max_rounds
		JOIN tuning_rounds r ON r.experiment_id = e.id AND r.round = t.round AND r.status = 'running'
		WHERE e.owner_id IS NOT NULL AND e.status IN ('active', 'paused')
		  AND r.ends_at > now() AND r.ends_at <= now() + interval '1 day'
		ON CONFLICT (user_id, dedupe) WHERE dedupe IS NOT NULL DO NOTHING`); err != nil {
		return 0, err
	}
	// The end date has come: stop.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, status FROM experiments WHERE kind = 'ab' AND status IN ('active', 'paused') AND end_at <= now()`)
	if err != nil {
		return 0, err
	}
	type due struct {
		id     int64
		status string
	}
	var ends []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.status); err != nil {
			rows.Close()
			return 0, err
		}
		ends = append(ends, d)
	}
	rows.Close()
	n := 0
	for _, d := range ends {
		if err := s.autoStop(ctx, d.id, d.status); err != nil {
			log.Printf("schedule: stop %d: %v", d.id, err)
			continue
		}
		n++
	}
	return n, nil
}

func (s *Server) autoStop(ctx context.Context, id int64, from string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE experiments SET status = 'stopped', buckets = '{}', ended_at = COALESCE(ended_at, now()), updated_at = now()
		WHERE id = $1 AND status IN ('active', 'paused') AND end_at <= now()`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := cancelPlans(ctx, tx, id, "", "the experiment reached its end date"); err != nil {
		return err
	}
	if err := tuning.Finish(ctx, tx, id, time.Now().UTC(), "Ended on its end date."); err != nil {
		return err
	}
	if err := audit(ctx, tx, 0, id, "experiment", id, "auto_stop", from, assign.StatusStopped, map[string]any{"note": "reached its end date"}); err != nil {
		return err
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ScheduleLoop runs RunSchedule every interval.
func (s *Server) ScheduleLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.RunSchedule(ctx); err != nil {
			if !strings.Contains(err.Error(), "context canceled") {
				log.Printf("schedule: %v", err)
			}
		} else if n > 0 {
			log.Printf("schedule: %d experiment(s) reached their end date and stopped", n)
			s.reload(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
