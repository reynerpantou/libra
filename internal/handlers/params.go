package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
)

// ParamUsage answers, for every field an experiment's variants set: which
// other experiments touch the same field, and would they conflict?
//
//   - Same field, or one sets a whole object the other sets a field inside
//     ("search.ranking" vs "search.ranking.formula"): they overlap. If both
//     can reach the same unit, only one value wins — by the priority rules.
//   - Only a common parent ("search.ranking.formula" vs "search.ads.slot"):
//     they write different fields and never conflict.
//   - Experiments in the same layer never share a unit, so never conflict.

type usageExp struct {
	ID         int64
	Name       string
	Status     string
	LayerID    int64
	LayerName  string
	StartedAt  *time.Time
	LaunchedAt *time.Time
	paths      map[string][]string // leaf path -> variant keys setting it
	exp        *assign.Experiment  // for priority comparison
}

type ParamUse struct {
	ExperimentID int64    `json:"experiment_id"`
	Experiment   string   `json:"experiment"`
	Status       string   `json:"status"`
	Layer        string   `json:"layer"`
	SameLayer    bool     `json:"same_layer"`
	Relation     string   `json:"relation"`    // same | inside | covers | shares_parent (only shares_parent is conflict-free)
	TheirPaths   []string `json:"their_paths"` // their fields involved
	Variants     []string `json:"variants"`    // their variants that set them
	Conflict     bool     `json:"conflict"`
	Winner       string   `json:"winner"` // this | other | none
	Reason       string   `json:"reason"`
}

var relationRank = map[string]int{"shares_parent": 0, "covers": 1, "inside": 1, "same": 2}

// servingStatuses are the experiments whose parameters can reach users now
// or later (not stopped or archived).
const servingStatuses = `('draft', 'in_review', 'approved', 'rejected', 'active', 'paused', 'launched')`

func (s *Server) ParamUsage(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT e.id, e.name, e.status, e.layer_id, l.name, e.started_at, e.launched_at, v.key, v.params
		FROM experiments e JOIN layers l ON l.id = e.layer_id JOIN variants v ON v.experiment_id = e.id
		WHERE e.id = $1 OR e.status IN `+servingStatuses+`
		ORDER BY e.id, v.position`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	exps := map[int64]*usageExp{}
	var order []int64
	for rows.Next() {
		var e usageExp
		var key string
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Name, &e.Status, &e.LayerID, &e.LayerName, &e.StartedAt, &e.LaunchedAt, &key, &raw); err != nil {
			serverError(w, r, err)
			return
		}
		cur := exps[e.ID]
		if cur == nil {
			e.paths = map[string][]string{}
			e.exp = &assign.Experiment{ID: e.ID}
			if e.StartedAt != nil {
				e.exp.StartOrder = e.StartedAt.Unix()
			}
			cur = &e
			exps[e.ID] = cur
			order = append(order, e.ID)
		}
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		for _, p := range assign.ParamPaths(params) {
			cur.paths[p] = append(cur.paths[p], key)
		}
	}
	if err := rows.Err(); err != nil {
		serverError(w, r, err)
		return
	}
	me := exps[id]
	if me == nil {
		var n int
		if err := s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM experiments WHERE id = $1`, id).Scan(&n); err != nil || n == 0 {
			notFound(w, "experiment")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"priority_rules": assign.PriorityRules, "paths": map[string]any{}})
		return
	}

	// Every node of this experiment's parameter trees: objects and leaves.
	nodes := map[string]bool{}
	for leaf := range me.paths {
		parts := strings.Split(leaf, ".")
		for i := 1; i <= len(parts); i++ {
			nodes[strings.Join(parts[:i], ".")] = true
		}
	}
	// Our own leaf fields at or under each node.
	mine := map[string][]string{}
	for leaf := range me.paths {
		for node := range nodes {
			if leaf == node || strings.HasPrefix(leaf, node+".") {
				mine[node] = append(mine[node], leaf)
			}
		}
	}
	out := map[string][]ParamUse{}
	for node := range nodes {
		uses := []ParamUse{}
		parent := ""
		if i := strings.LastIndex(node, "."); i >= 0 {
			parent = node[:i]
		}
		for _, oid := range order {
			o := exps[oid]
			if oid == id {
				continue
			}
			use := ParamUse{ExperimentID: o.ID, Experiment: o.Name, Status: o.Status, Layer: o.LayerName, SameLayer: o.LayerID == me.LayerID, Relation: ""}
			variants := map[string]bool{}
			for q, vks := range o.paths {
				rel := ""
				switch {
				case q == node:
					rel = "same"
				case strings.HasPrefix(node, q+"."):
					rel = "covers" // they set a whole value above this field
				case strings.HasPrefix(q, node+"."):
					// Inside this object: a conflict only if it touches one of
					// our own fields; other fields just share the object.
					if overlapsAny(q, mine[node]) {
						rel = "inside"
					} else {
						rel = "shares_parent"
					}
				case parent != "" && strings.HasPrefix(q, parent+"."):
					rel = "shares_parent"
				default:
					continue
				}
				if use.Relation == "" || relationRank[rel] > relationRank[use.Relation] {
					use.Relation = rel
					use.TheirPaths = nil
				}
				if rel == use.Relation && len(use.TheirPaths) < 6 {
					use.TheirPaths = append(use.TheirPaths, q)
				}
				if rel == use.Relation {
					for _, k := range vks {
						variants[k] = true
					}
				}
			}
			if use.Relation == "" {
				continue
			}
			for k := range variants {
				use.Variants = append(use.Variants, k)
			}
			sort.Strings(use.Variants)
			sort.Strings(use.TheirPaths)
			judge(me, o, &use)
			uses = append(uses, use)
		}
		sort.SliceStable(uses, func(i, j int) bool {
			if uses[i].Conflict != uses[j].Conflict {
				return uses[i].Conflict
			}
			return relationRank[uses[i].Relation] > relationRank[uses[j].Relation]
		})
		if len(uses) > 0 {
			out[node] = uses
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"priority_rules": assign.PriorityRules, "paths": out})
}

// judge decides whether an overlap is a real conflict and who wins it.
func judge(me, o *usageExp, u *ParamUse) {
	u.Winner = "none"
	if u.Relation == "shares_parent" {
		u.Reason = "Only the parent object is shared; the experiments set different fields, so both values apply."
		return
	}
	meLaunch, oLaunch := me.Status == assign.StatusLaunched, o.Status == assign.StatusLaunched
	if u.SameLayer && !meLaunch && !oLaunch {
		u.Reason = "Same layer: the experiments never share a unit, so each unit only ever gets one of the values."
		return
	}
	u.Conflict = true
	if me.Status == assign.StatusStopped || me.Status == assign.StatusArchived {
		u.Winner = "other"
		u.Reason = "This experiment is finished, so it no longer serves; the other experiment's value is what units get."
		return
	}
	switch {
	case meLaunch && oLaunch:
		if later(me.LaunchedAt, o.LaunchedAt) {
			u.Winner, u.Reason = "this", "Both are launched defaults; this one launched more recently, so its value wins."
		} else {
			u.Winner, u.Reason = "other", "Both are launched defaults; the other launched more recently, so its value wins."
		}
	case oLaunch:
		u.Winner, u.Reason = "this", "The other experiment is a launched default; a running experiment overrides launched values for its units."
	case meLaunch:
		u.Winner, u.Reason = "other", "This experiment is a launched default; the other experiment overrides it for units it assigns."
	case assign.Before(me.exp, o.exp):
		u.Winner, u.Reason = "this", startedReason(me, o, true)
	default:
		u.Winner, u.Reason = "other", startedReason(me, o, false)
	}
	if !u.SameLayer {
		u.Reason += " This only matters for units that match both experiments' targeting and traffic."
	}
}

func startedReason(me, o *usageExp, meWins bool) string {
	switch {
	case me.StartedAt == nil && o.StartedAt == nil:
		if meWins {
			return "Neither has started; if both run, the one that starts first wins — for now this one (smaller id breaks the tie)."
		}
		return "Neither has started; if both run, the one that starts first wins — for now the other one (smaller id breaks the tie)."
	case me.StartedAt == nil:
		return "The other experiment started already and this one hasn't, so the other one's value wins (the earlier start wins)."
	case o.StartedAt == nil:
		return "This experiment started and the other hasn't, so this one's value wins (the earlier start wins)."
	case meWins:
		return "This experiment started first (" + me.StartedAt.UTC().Format("2006-01-02 15:04") + " vs " + o.StartedAt.UTC().Format("2006-01-02 15:04") + " UTC), so its value wins."
	default:
		return "The other experiment started first (" + o.StartedAt.UTC().Format("2006-01-02 15:04") + " vs " + me.StartedAt.UTC().Format("2006-01-02 15:04") + " UTC), so its value wins."
	}
}

// overlapsAny reports whether field q is, contains, or sits inside any of
// the given fields.
func overlapsAny(q string, fields []string) bool {
	for _, f := range fields {
		if q == f || strings.HasPrefix(q, f+".") || strings.HasPrefix(f, q+".") {
			return true
		}
	}
	return false
}

func later(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.After(*b)
}
