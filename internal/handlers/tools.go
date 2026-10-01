package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/pipeline"
)

// Diagnose explains, experiment by experiment, what a unit gets and why —
// the "why didn't this user hit my experiment?" tool. Nothing is logged.
func (s *Server) Diagnose(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	snap := s.Store.Snapshot()
	// The same answer a service gets from /v1/resolve (nothing logged),
	// plus the trace explaining each experiment.
	out, res, msg := resolveFor(snap, req, true)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	trace := out.Trace
	out.Trace = nil
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot_version": snap.Version,
		"request":          req,
		"response":         out,
		"result":           map[string]any{"hits": res.Hits, "params": res.Params, "trace": trace, "conflicts": res.Conflicts},
	})
}

// ParamSearch finds which experiments set a parameter path.
func (s *Server) ParamSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	type hit struct {
		ExperimentID int64  `json:"experiment_id"`
		Experiment   string `json:"experiment"`
		Platform     string `json:"platform"`
		Business     string `json:"business"`
		Status       string `json:"status"`
		Variant      string `json:"variant"`
		Path         string `json:"path"`
	}
	out := []hit{}
	for _, e := range s.Store.Snapshot().Experiments {
		for _, v := range e.Variants {
			for _, p := range assign.ParamPaths(v.Params) {
				if q == "" || strings.Contains(strings.ToLower(p), q) {
					out = append(out, hit{e.ID, e.Name, e.PlatformKey, e.BusinessKey, e.Status, v.Key, p})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > 500 {
		out = out[:500]
	}
	writeJSON(w, http.StatusOK, out)
}

// PipelineStatus shows the pipeline's state and recent runs.
func (s *Server) PipelineStatus(w http.ResponseWriter, r *http.Request) {
	type run struct {
		ID         int64      `json:"id"`
		Trigger    string     `json:"trigger"`
		StartedAt  time.Time  `json:"started_at"`
		FinishedAt *time.Time `json:"finished_at"`
		Status     string     `json:"status"`
		Stats      []byte     `json:"-"`
		StatsJSON  any        `json:"stats"`
		Error      string     `json:"error"`
	}
	out := map[string]any{}
	var expWM, evWM, expMax, evMax, assignments, dailyRows int64
	_ = s.DB.QueryRowContext(r.Context(), `SELECT exposure_watermark, event_watermark FROM pipeline_state`).Scan(&expWM, &evWM)
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COALESCE(max(id), 0) FROM exposures`).Scan(&expMax)
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COALESCE(max(id), 0) FROM events`).Scan(&evMax)
	_ = s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM assignments`).Scan(&assignments)
	_ = s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM unit_measure_daily`).Scan(&dailyRows)
	var pending int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM measures WHERE needs_backfill`).Scan(&pending)
	out["exposures_pending"] = expMax - expWM
	out["events_pending"] = evMax - evWM
	out["exposures_total"] = expMax
	out["events_total"] = evMax
	out["assignments"] = assignments
	out["measure_rows"] = dailyRows
	out["measures_pending_backfill"] = pending
	out["interval_seconds"] = int(s.Cfg.PipelineInterval.Seconds())
	if s.Exposures != nil {
		out["exposures_dropped"] = s.Exposures.Dropped.Load()
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, trigger, started_at, finished_at, status, stats, error FROM pipeline_runs ORDER BY id DESC LIMIT 30`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	runs := []run{}
	for rows.Next() {
		var x run
		if err := rows.Scan(&x.ID, &x.Trigger, &x.StartedAt, &x.FinishedAt, &x.Status, &x.Stats, &x.Error); err != nil {
			serverError(w, r, err)
			return
		}
		x.StatsJSON = rawJSON(x.Stats)
		runs = append(runs, x)
	}
	out["runs"] = runs
	writeJSON(w, http.StatusOK, out)
}

// RunPipeline runs the pipeline now and waits for it (bounded).
func (s *Server) RunPipeline(w http.ResponseWriter, r *http.Request) {
	st, err := pipeline.Run(r.Context(), s.DB, "manual")
	if errors.Is(err, pipeline.ErrBusy) {
		writeError(w, http.StatusConflict, "busy", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pipeline_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type rawJSON []byte

func (j rawJSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}
