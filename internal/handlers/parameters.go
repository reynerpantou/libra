package handlers

import (
	"net/http"
	"sort"

	"github.com/reynerpantou/libra/internal/assign"
)

// ParamValue is one leaf parameter served by one variant of an experiment.
type ParamValue struct {
	Path         string `json:"path"`
	Value        any    `json:"value"`
	ExperimentID int64  `json:"experiment_id"`
	Experiment   string `json:"experiment"`
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
		pv := ParamValue{ExperimentID: e.ID, Experiment: e.Name, Business: e.BusinessKey, Status: e.Status}
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
