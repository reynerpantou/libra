package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/report"
)

func reportOptions(r *http.Request) (report.Options, string) {
	q := r.URL.Query()
	var o report.Options
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &o.From}, {"to", &o.To}} {
		if v := q.Get(f.name); v != "" {
			t, err := time.Parse("2006-01-02", v)
			if err != nil {
				return o, f.name + " must be YYYY-MM-DD"
			}
			*f.dst = t
		}
	}
	if !o.From.IsZero() && !o.To.IsZero() && o.To.Before(o.From) {
		return o, "the end date is before the start date"
	}
	if v := q.Get("metrics"); v != "" {
		for _, part := range strings.Split(v, ",") {
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return o, "metrics must be a list of ids"
			}
			o.MetricIDs = append(o.MetricIDs, id)
		}
	}
	if v := q.Get("alpha"); v != "" {
		a, err := strconv.ParseFloat(v, 64)
		if err != nil || a <= 0 || a >= 0.5 {
			return o, "alpha must be between 0 and 0.5"
		}
		o.Alpha = a
	}
	o.Dimension = strings.TrimSpace(q.Get("dimension"))
	if len(o.Dimension) > 40 {
		return o, "dimension name is too long"
	}
	return o, ""
}

func (s *Server) ExperimentReport(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	o, msg := reportOptions(r)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	rep, err := report.Compute(r.Context(), s.DB, id, o)
	if errors.Is(err, report.ErrNotFound) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) ExperimentTrend(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	o, msg := reportOptions(r)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	metric, err := strconv.ParseInt(r.URL.Query().Get("metric"), 10, 64)
	if err != nil {
		badRequest(w, "metric is required")
		return
	}
	points, err := report.Trend(r.Context(), s.DB, id, metric, o)
	if errors.Is(err, report.ErrNotFound) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, points)
}
