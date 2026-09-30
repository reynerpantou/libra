package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
)

// ParamValue is one leaf parameter served by one variant of an experiment.
type ParamValue struct {
	Path         string `json:"path"`
	Value        any    `json:"value"`
	ExperimentID int64  `json:"experiment_id"`
	Experiment   string `json:"experiment"`
	Platform     string `json:"platform"`
	Business     string `json:"business"`
	Status       string `json:"status"` // active | launched
	VariantKey   string `json:"variant_key"`
	VariantName  string `json:"variant_name"`
	IsControl    bool   `json:"is_control"`
	Layer        string `json:"layer"`
	LayerAuto    bool   `json:"layer_auto"`
	Diversion    string `json:"diversion"`
	Traffic      int    `json:"traffic"` // per mille of the layer held (running experiments)
	// Default: this launched value is what everyone gets for the path unless
	// a running experiment overrides it (the most recent launch wins).
	Default bool `json:"default"`
}

// ListParameters lists every parameter currently served: the variants of
// running experiments and the chosen variants of launched ones.
func (s *Server) ListParameters(w http.ResponseWriter, r *http.Request) {
	snap := s.Store.Snapshot()
	out := []ParamValue{}
	defaultOf := map[string]int{} // path -> index in out of the winning launch
	for _, e := range snap.Experiments {
		if e.Status != assign.StatusActive && e.Status != assign.StatusLaunched {
			continue
		}
		pv := ParamValue{ExperimentID: e.ID, Experiment: e.Name, Platform: e.PlatformKey, Business: e.BusinessKey, Status: e.Status}
		if l := snap.Layers[e.LayerID]; l != nil {
			pv.Layer, pv.LayerAuto, pv.Diversion = l.Name, l.Auto, l.Diversion
		}
		if e.Status == assign.StatusActive {
			pv.Traffic = len(e.Buckets)
		}
		for _, v := range e.Variants {
			if e.Status == assign.StatusLaunched && v.ID != e.LaunchedVar {
				continue
			}
			x := pv
			x.VariantKey, x.VariantName, x.IsControl = v.Key, v.Name, v.IsControl
			for path, val := range assign.FlattenParams(v.Params) {
				y := x
				y.Path, y.Value = path, val
				if e.Status == assign.StatusLaunched {
					if i, ok := defaultOf[path]; !ok || launchedLater(e, out[i], snap) {
						defaultOf[path] = len(out)
					}
				}
				out = append(out, y)
			}
		}
	}
	for _, i := range defaultOf {
		out[i].Default = true
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].ExperimentID < out[j].ExperimentID
	})
	writeJSON(w, http.StatusOK, out)
}

func launchedLater(e *assign.Experiment, cur ParamValue, snap *assign.Snapshot) bool {
	for _, o := range snap.Experiments {
		if o.ID == cur.ExperimentID {
			return e.LaunchOrder > o.LaunchOrder || (e.LaunchOrder == o.LaunchOrder && e.ID > o.ID)
		}
	}
	return true
}

// LaunchedField is one leaf of a business's launched (default) config.
type LaunchedField struct {
	Path         string          `json:"path"`
	Value        any             `json:"value"`
	ExperimentID int64           `json:"experiment_id"`
	Experiment   string          `json:"experiment"`
	Variant      string          `json:"variant"`
	LaunchedAt   time.Time       `json:"launched_at"`
	Rollout      int             `json:"rollout"`  // per mille of units that get it
	Targeted     bool            `json:"targeted"` // only units matching the experiment's targeting
	Replaced     []LaunchedValue `json:"replaced"` // earlier launches this one overrides
}

type LaunchedValue struct {
	Path         string    `json:"path"`
	Value        any       `json:"value"`
	ExperimentID int64     `json:"experiment_id"`
	Experiment   string    `json:"experiment"`
	LaunchedAt   time.Time `json:"launched_at"`
}

type LaunchRecord struct {
	ExperimentID int64          `json:"experiment_id"`
	Experiment   string         `json:"experiment"`
	Platform     string         `json:"platform"`
	Business     string         `json:"business"`
	Status       string         `json:"status"` // launched (serving) | archived (retired)
	Variant      string         `json:"variant"`
	VariantName  string         `json:"variant_name"`
	LaunchedAt   time.Time      `json:"launched_at"`
	Rollout      int            `json:"rollout"`
	Targeted     bool           `json:"targeted"`
	Params       map[string]any `json:"params"`
	Live         int            `json:"live_fields"` // fields still in effect (not replaced by a later launch)
	Fields       int            `json:"fields"`
}

// LaunchedConfig is the released configuration: for each business, the
// defaults everyone gets from launched variants (later launches override
// earlier ones), and the history of every launch, including retired ones.
func (s *Server) LaunchedConfig(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT e.id, e.name, p.key, b.key, p.name || ' › ' || b.name, e.status, v.key, v.name, v.params, e.launched_at, e.launch_rollout, e.targeting
		FROM experiments e
		JOIN businesses b ON b.id = e.business_id
		JOIN platforms p ON p.id = b.platform_id
		JOIN variants v ON v.id = e.launched_variant_id
		WHERE e.launched_at IS NOT NULL AND e.status IN ('launched', 'archived')
		ORDER BY e.launched_at, e.id`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type biz struct {
		Key      string                    `json:"key"` // platform/business: unique
		Platform string                    `json:"platform"`
		Business string                    `json:"business"`
		Name     string                    `json:"name"`
		Fields   []*LaunchedField          `json:"fields"`
		byPath   map[string]*LaunchedField `json:"-"`
	}
	var order []string
	byBiz := map[string]*biz{}
	history := []*LaunchRecord{}
	recs := map[int64]*LaunchRecord{}
	for rows.Next() {
		var rec LaunchRecord
		var bizName string
		var params, targeting []byte
		if err := rows.Scan(&rec.ExperimentID, &rec.Experiment, &rec.Platform, &rec.Business, &bizName, &rec.Status, &rec.Variant, &rec.VariantName, &params,
			&rec.LaunchedAt, &rec.Rollout, &targeting); err != nil {
			serverError(w, r, err)
			return
		}
		_ = json.Unmarshal(params, &rec.Params)
		if rec.Params == nil {
			rec.Params = map[string]any{}
		}
		var t assign.Targeting
		_ = json.Unmarshal(targeting, &t)
		rec.Targeted = len(t.Groups) > 0
		flat := assign.FlattenParams(rec.Params)
		rec.Fields = len(flat)
		history = append(history, &rec)
		recs[rec.ExperimentID] = &rec
		if rec.Status != assign.StatusLaunched {
			continue // retired: no longer served
		}
		key := rec.Platform + "/" + rec.Business
		b := byBiz[key]
		if b == nil {
			b = &biz{Key: key, Platform: rec.Platform, Business: rec.Business, Name: bizName, byPath: map[string]*LaunchedField{}}
			byBiz[key] = b
			order = append(order, key)
		}
		paths := make([]string, 0, len(flat))
		for p := range flat {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			f := &LaunchedField{Path: p, Value: flat[p], ExperimentID: rec.ExperimentID, Experiment: rec.Experiment, Variant: rec.Variant,
				LaunchedAt: rec.LaunchedAt, Rollout: rec.Rollout, Targeted: rec.Targeted, Replaced: []LaunchedValue{}}
			// The same field, a whole object above it, or fields inside it:
			// all replaced by this later launch.
			for q, old := range b.byPath {
				if q == p || strings.HasPrefix(q, p+".") || strings.HasPrefix(p, q+".") {
					f.Replaced = append(f.Replaced, LaunchedValue{q, old.Value, old.ExperimentID, old.Experiment, old.LaunchedAt})
					f.Replaced = append(f.Replaced, old.Replaced...)
					delete(b.byPath, q)
				}
			}
			b.byPath[p] = f
		}
	}
	out := []biz{}
	for _, k := range order {
		b := byBiz[k]
		for _, f := range b.byPath {
			b.Fields = append(b.Fields, f)
			recs[f.ExperimentID].Live++
		}
		sort.Slice(b.Fields, func(i, j int) bool { return b.Fields[i].Path < b.Fields[j].Path })
		out = append(out, *b)
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].LaunchedAt.After(history[j].LaunchedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"businesses": out, "history": history})
}
