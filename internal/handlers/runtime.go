package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/serving"
)

// MaxBatch caps items per ingestion request.
const MaxBatch = 5000

type resolveRequest struct {
	UnitID   string         `json:"unit_id"`
	Business string         `json:"business,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
	// LogExposure records exposures (default true). Set false when the
	// caller only prefetches config and will report exposures itself.
	LogExposure *bool `json:"log_exposure,omitempty"`
	Debug       bool  `json:"debug,omitempty"`
}

type resolveResponse struct {
	UnitID          string            `json:"unit_id"`
	SnapshotVersion int64             `json:"snapshot_version"`
	Params          map[string]any    `json:"params"`
	Hits            []assign.Hit      `json:"hits"`
	Conflicts       []assign.Conflict `json:"conflicts,omitempty"`
	Trace           []assign.Step     `json:"trace,omitempty"`
}

// Resolve returns the unit's experiments and merged parameters, and logs
// exposures for units assigned by traffic (not whitelist or launches).
func (s *Server) Resolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := decodeLimit(r, &req, 64<<10); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	req.UnitID = strings.TrimSpace(req.UnitID)
	if req.UnitID == "" || len(req.UnitID) > 200 {
		badRequest(w, "unit_id is required (up to 200 characters)")
		return
	}
	snap := s.Store.Snapshot()
	res := snap.Resolve(assign.Request{UnitID: req.UnitID, Business: req.Business, Attrs: req.Attrs}, req.Debug)
	if req.LogExposure == nil || *req.LogExposure {
		now := time.Now().UTC()
		attrs := serving.DimensionAttrs(req.Attrs)
		for _, h := range res.Hits {
			if h.Source == assign.SourceTraffic {
				s.Exposures.Log(serving.Exposure{ExperimentID: h.ExperimentID, VariantID: h.VariantID, UnitID: req.UnitID, TS: now, Attrs: attrs})
			}
		}
	}
	writeJSON(w, http.StatusOK, resolveResponse{
		UnitID: req.UnitID, SnapshotVersion: snap.Version, Params: res.Params, Hits: res.Hits,
		Conflicts: res.Conflicts, Trace: res.Trace,
	})
}

type exposureIn struct {
	ExperimentID int64          `json:"experiment_id"`
	VariantID    int64          `json:"variant_id"`
	UnitID       string         `json:"unit_id"`
	TS           *time.Time     `json:"ts,omitempty"`
	Attrs        map[string]any `json:"attrs,omitempty"`
}

// IngestExposures accepts exposures reported by clients that resolved
// earlier with log_exposure=false (e.g. logging only when a screen renders).
// Only running experiments' real variants are accepted.
func (s *Server) IngestExposures(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Exposures []exposureIn `json:"exposures"`
	}
	if err := decodeLimit(r, &req, 16<<20); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	if len(req.Exposures) == 0 || len(req.Exposures) > MaxBatch {
		badRequest(w, fmt.Sprintf("send 1 to %d exposures", MaxBatch))
		return
	}
	valid := map[[2]int64]bool{}
	for _, e := range s.Store.Snapshot().Experiments {
		if e.Status == assign.StatusActive || e.Status == assign.StatusPaused {
			for _, v := range e.Variants {
				valid[[2]int64{e.ID, v.ID}] = true
			}
		}
	}
	now := time.Now().UTC()
	var batch []serving.Exposure
	var rejected []map[string]any
	for i, x := range req.Exposures {
		unit := strings.TrimSpace(x.UnitID)
		switch {
		case unit == "" || len(unit) > 200:
			rejected = append(rejected, map[string]any{"index": i, "error": "unit_id is required"})
		case !valid[[2]int64{x.ExperimentID, x.VariantID}]:
			rejected = append(rejected, map[string]any{"index": i, "error": "not a variant of a running experiment"})
		default:
			ts := now
			if x.TS != nil && x.TS.Before(now.Add(time.Minute)) && x.TS.After(now.AddDate(0, 0, -7)) {
				ts = x.TS.UTC()
			}
			batch = append(batch, serving.Exposure{ExperimentID: x.ExperimentID, VariantID: x.VariantID, UnitID: unit, TS: ts, Attrs: serving.DimensionAttrs(x.Attrs)})
		}
	}
	if err := serving.WriteExposures(r.Context(), s.DB, batch); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(batch), "rejected": rejectedOrEmpty(rejected)})
}

type eventIn struct {
	Business string          `json:"business"`
	Event    string          `json:"event"`
	UnitID   string          `json:"unit_id"`
	TS       *time.Time      `json:"ts,omitempty"`
	Value    *float64        `json:"value,omitempty"`
	Props    json.RawMessage `json:"props,omitempty"`
}

// IngestEvents accepts business events: the raw material measures are
// computed from. Timestamps up to 30 days old are accepted (backfills);
// the pipeline recomputes whichever days receive events.
func (s *Server) IngestEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Events []eventIn `json:"events"`
	}
	if err := decodeLimit(r, &req, 16<<20); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	if len(req.Events) == 0 || len(req.Events) > MaxBatch {
		badRequest(w, fmt.Sprintf("send 1 to %d events", MaxBatch))
		return
	}
	now := time.Now().UTC()
	var batch []serving.Event
	var rejected []map[string]any
	for i, x := range req.Events {
		bid, err := s.businessID(r.Context(), x.Business)
		if err != nil {
			serverError(w, r, err)
			return
		}
		unit := strings.TrimSpace(x.UnitID)
		name := strings.TrimSpace(x.Event)
		props := []byte(x.Props)
		var obj map[string]any
		switch {
		case bid == 0:
			rejected = append(rejected, map[string]any{"index": i, "error": "unknown business " + x.Business})
			continue
		case name == "" || len(name) > 120:
			rejected = append(rejected, map[string]any{"index": i, "error": "event is required"})
			continue
		case unit == "" || len(unit) > 200:
			rejected = append(rejected, map[string]any{"index": i, "error": "unit_id is required"})
			continue
		case len(props) > 0 && (json.Unmarshal(props, &obj) != nil || obj == nil):
			rejected = append(rejected, map[string]any{"index": i, "error": "props must be a JSON object"})
			continue
		case len(props) > 8<<10:
			rejected = append(rejected, map[string]any{"index": i, "error": "props larger than 8 KB"})
			continue
		}
		ts := now
		if x.TS != nil {
			if x.TS.After(now.Add(5*time.Minute)) || x.TS.Before(now.AddDate(0, 0, -30)) {
				rejected = append(rejected, map[string]any{"index": i, "error": "ts must be within the last 30 days"})
				continue
			}
			ts = x.TS.UTC()
		}
		val := 0.0
		if x.Value != nil {
			val = *x.Value
		}
		batch = append(batch, serving.Event{BusinessID: bid, Name: name, UnitID: unit, TS: ts, Value: val, Props: props})
	}
	if err := serving.WriteEvents(r.Context(), s.DB, batch); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(batch), "rejected": rejectedOrEmpty(rejected)})
}

func rejectedOrEmpty(r []map[string]any) []map[string]any {
	if r == nil {
		return []map[string]any{}
	}
	if len(r) > 100 {
		return r[:100]
	}
	return r
}

// business keys are cached briefly: ingestion is hot, businesses rarely change.
var bizCache struct {
	sync.Mutex
	ids    map[string]int64
	loaded time.Time
}

func (s *Server) businessID(ctx context.Context, key string) (int64, error) {
	bizCache.Lock()
	defer bizCache.Unlock()
	if bizCache.ids == nil || time.Since(bizCache.loaded) > 30*time.Second || (bizCache.ids[key] == 0 && time.Since(bizCache.loaded) > 2*time.Second) {
		rows, err := s.DB.QueryContext(ctx, `SELECT key, id FROM businesses`)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		ids := map[string]int64{}
		for rows.Next() {
			var k string
			var id int64
			if err := rows.Scan(&k, &id); err != nil {
				return 0, err
			}
			ids[k] = id
		}
		bizCache.ids, bizCache.loaded = ids, time.Now()
	}
	return bizCache.ids[key], nil
}
