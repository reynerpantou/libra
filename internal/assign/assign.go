// Package assign decides which experiments and variants a unit (a user or
// device id) gets. It's pure logic over an in-memory Snapshot, so the same
// code serves requests, powers hit diagnosis, and drives the simulator.
//
// Traffic model:
//
//   - Every experiment lives in a layer. A layer hashes each unit into one of
//     Buckets buckets, and each bucket belongs to at most one experiment, so
//     experiments in the same layer never share units (mutual exclusion).
//     Experiments in different layers are independent (orthogonal).
//   - An experiment's traffic is the set of buckets it holds. Ramping up adds
//     buckets and ramping down removes the most recently added ones, so units
//     already in the experiment stay in it.
//   - Inside the experiment a second, independent hash picks the variant by
//     per-mille weights. Weights are locked once an experiment starts.
package assign

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Buckets is the number of traffic buckets per layer (0.1% each).
const Buckets = 1000

// Statuses an experiment can be in.
const (
	StatusDraft     = "draft"
	StatusInReview  = "in_review"
	StatusApproved  = "approved"
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusStopped   = "stopped"
	StatusLaunched  = "launched"
	StatusArchived  = "archived"
	StatusRejected  = "rejected"
	SourceTraffic   = "experiment"
	SourceWhitelist = "whitelist"
	SourceLaunch    = "launch"
)

// Diversion is which id a layer randomizes on.
const (
	DiversionUser   = "user_id"
	DiversionDevice = "device_id"
)

// Rule is one targeting condition.
type Rule struct {
	Attr   string   `json:"attr"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// Targeting is OR of AND-groups: a unit matches when every rule of at least
// one group passes. No groups means everyone matches.
//
//	(device = android AND app_version >= 3.400) OR (device = ios AND app_version >= 2.300) OR device in (desktop, mobile)
type Targeting struct {
	Groups [][]Rule `json:"groups"`
}

// UnmarshalJSON also accepts the older form, a flat list of rules that all
// had to match, as a single group.
func (t *Targeting) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var rules []Rule
		if err := json.Unmarshal(b, &rules); err != nil {
			return err
		}
		t.Groups = nil
		if len(rules) > 0 {
			t.Groups = [][]Rule{rules}
		}
		return nil
	}
	var raw struct {
		Groups [][]Rule `json:"groups"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	t.Groups = raw.Groups
	return nil
}

// MarshalJSON always writes groups as a list, never null.
func (t Targeting) MarshalJSON() ([]byte, error) {
	g := t.Groups
	if g == nil {
		g = [][]Rule{}
	}
	return json.Marshal(struct {
		Groups [][]Rule `json:"groups"`
	}{g})
}

// Ops lists the supported targeting operators.
var Ops = []string{"eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte",
	"version_eq", "version_gt", "version_gte", "version_lt", "version_lte", "exists", "not_exists"}

type Variant struct {
	ID        int64          `json:"id"`
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	IsControl bool           `json:"is_control"`
	Weight    int            `json:"weight"` // per mille; an experiment's weights sum to 1000
	Params    map[string]any `json:"params"`
}

type Experiment struct {
	ID          int64
	BusinessKey string
	LayerID     int64
	Name        string
	Status      string
	Salt        string
	Buckets     []int // held buckets, in the order they were added
	Targeting   Targeting
	Variants    []Variant
	Whitelist   map[string]int64 // unit id (of the layer's diversion) -> forced variant id
	LaunchedVar int64            // for launched experiments
	LaunchOrder int64            // launch time (unix) — later launches override earlier ones
	StartOrder  int64            // start time (unix); 0 = never started
}

type Layer struct {
	ID        int64
	Name      string
	Salt      string
	Diversion string // user_id | device_id
	owner     [Buckets]*Experiment
}

// Snapshot is an immutable view of everything serving needs.
type Snapshot struct {
	Version     int64
	Layers      map[int64]*Layer
	Experiments []*Experiment // sorted by id
}

// NewSnapshot indexes experiments into their layers' bucket tables. Only
// active experiments hold traffic.
func NewSnapshot(version int64, layers []*Layer, exps []*Experiment) *Snapshot {
	s := &Snapshot{Version: version, Layers: map[int64]*Layer{}}
	for _, l := range layers {
		s.Layers[l.ID] = l
	}
	sort.Slice(exps, func(i, j int) bool { return exps[i].ID < exps[j].ID })
	s.Experiments = exps
	for _, e := range exps {
		l := s.Layers[e.LayerID]
		if l == nil || e.Status != StatusActive {
			continue
		}
		for _, b := range e.Buckets {
			if b >= 0 && b < Buckets && l.owner[b] == nil {
				l.owner[b] = e
			}
		}
	}
	return s
}

// Hash maps a string to [0, Buckets). SDKs that assign locally must use the
// same function: first 8 bytes of SHA-256, big-endian, modulo 1000.
func Hash(s string) int {
	sum := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint64(sum[:8]) % Buckets)
}

// LayerBucket is the unit's bucket in a layer.
func LayerBucket(layerSalt, unit string) int { return Hash("layer:" + layerSalt + ":" + unit) }

// VariantBucket is the unit's bucket inside an experiment.
func VariantBucket(expSalt, unit string) int { return Hash("exp:" + expSalt + ":" + unit) }

// PickVariant chooses a variant by per-mille weights.
func PickVariant(vs []Variant, bucket int) *Variant {
	acc := 0
	for i := range vs {
		acc += vs[i].Weight
		if bucket < acc {
			return &vs[i]
		}
	}
	return nil
}

// Request is what a caller knows about the unit. Each layer uses the id of
// its diversion type; UnitID is the older name for UserID.
type Request struct {
	UserID   string         `json:"user_id,omitempty"`
	DeviceID string         `json:"device_id,omitempty"`
	UnitID   string         `json:"unit_id,omitempty"`
	Business string         `json:"business,omitempty"` // only this business's experiments; empty = all
	Attrs    map[string]any `json:"attrs,omitempty"`
}

// ID returns the request's id for a diversion type.
func (r Request) ID(diversion string) string {
	if diversion == DiversionDevice {
		return r.DeviceID
	}
	if r.UserID != "" {
		return r.UserID
	}
	return r.UnitID
}

// Hit is one experiment the unit is in.
type Hit struct {
	ExperimentID int64  `json:"experiment_id"`
	Experiment   string `json:"experiment"`
	VariantID    int64  `json:"variant_id"`
	Variant      string `json:"variant"`
	Source       string `json:"source"`    // experiment | whitelist | launch
	UnitType     string `json:"unit_type"` // the id this assignment is keyed on
	UnitID       string `json:"unit_id,omitempty"`
}

// Step explains the decision for one experiment (hit diagnosis).
type Step struct {
	ExperimentID  int64  `json:"experiment_id"`
	Experiment    string `json:"experiment"`
	Business      string `json:"business"`
	Status        string `json:"status"`
	Outcome       string `json:"outcome"`
	Detail        string `json:"detail"`
	LayerBucket   int    `json:"layer_bucket"`
	VariantBucket int    `json:"variant_bucket"`
}

// Conflict records a parameter two experiments both tried to set.
type Conflict struct {
	Path   string `json:"path"`
	Winner int64  `json:"winner_experiment_id"`
	Loser  int64  `json:"loser_experiment_id"`
}

// Result is the full decision for a request.
type Result struct {
	Hits      []Hit          `json:"hits"`
	Params    map[string]any `json:"params"`
	Conflicts []Conflict     `json:"conflicts,omitempty"`
	Trace     []Step         `json:"trace,omitempty"`
}

// Resolve decides every experiment for the request. With trace set it also
// explains the outcome of each experiment, including the ones not hit.
func (s *Snapshot) Resolve(req Request, trace bool) Result {
	res := Result{Hits: []Hit{}}
	var launched, assigned []struct {
		e *Experiment
		v *Variant
	}
	for _, e := range s.Experiments {
		step := Step{ExperimentID: e.ID, Experiment: e.Name, Business: e.BusinessKey, Status: e.Status, LayerBucket: -1, VariantBucket: -1}
		record := func(outcome, detail string) {
			if trace {
				step.Outcome, step.Detail = outcome, detail
				res.Trace = append(res.Trace, step)
			}
		}
		if req.Business != "" && e.BusinessKey != req.Business {
			record("other_business", "experiment belongs to business "+e.BusinessKey)
			continue
		}
		l := s.Layers[e.LayerID]
		diversion := DiversionUser
		if l != nil && l.Diversion != "" {
			diversion = l.Diversion
		}
		unit := req.ID(diversion)
		// Whitelisted units get their variant whenever the experiment isn't
		// finished — including before it starts, which is how QA checks it.
		if vid, ok := e.Whitelist[unit]; ok && unit != "" && whitelistable(e.Status) {
			if v := variantByID(e, vid); v != nil {
				res.Hits = append(res.Hits, Hit{e.ID, e.Name, v.ID, v.Key, SourceWhitelist, diversion, unit})
				assigned = append(assigned, struct {
					e *Experiment
					v *Variant
				}{e, v})
				record("whitelisted", "unit is on the whitelist for variant "+v.Key+" (not counted in reports)")
				continue
			}
		}
		switch e.Status {
		case StatusActive, StatusLaunched:
		default:
			record("not_running", "experiment is "+e.Status)
			continue
		}
		if ok, why := Match(e.Targeting, req.Attrs); !ok {
			record("targeting_failed", why)
			continue
		}
		if e.Status == StatusLaunched {
			if v := variantByID(e, e.LaunchedVar); v != nil {
				res.Hits = append(res.Hits, Hit{e.ID, e.Name, v.ID, v.Key, SourceLaunch, diversion, unit})
				launched = append(launched, struct {
					e *Experiment
					v *Variant
				}{e, v})
				record("launched", "fully launched variant "+v.Key+" applies to everyone targeted")
			} else {
				record("not_running", "launched variant is missing")
			}
			continue
		}
		if l == nil {
			record("not_in_traffic", "layer is missing")
			continue
		}
		if unit == "" {
			record("missing_id", "this experiment's layer splits traffic by "+diversion+", and the request has none")
			continue
		}
		lb := LayerBucket(l.Salt, unit)
		step.LayerBucket = lb
		if owner := l.owner[lb]; owner != e {
			if owner == nil {
				record("not_in_traffic", fmt.Sprintf("layer bucket %d isn't allocated to this experiment (it holds %d of %d buckets)", lb, len(e.Buckets), Buckets))
			} else {
				record("not_in_traffic", fmt.Sprintf("layer bucket %d belongs to experiment #%d %q in the same layer", lb, owner.ID, owner.Name))
			}
			continue
		}
		vb := VariantBucket(e.Salt, unit)
		step.VariantBucket = vb
		v := PickVariant(e.Variants, vb)
		if v == nil {
			record("not_in_traffic", fmt.Sprintf("variant bucket %d is beyond the variant weights", vb))
			continue
		}
		res.Hits = append(res.Hits, Hit{e.ID, e.Name, v.ID, v.Key, SourceTraffic, diversion, unit})
		assigned = append(assigned, struct {
			e *Experiment
			v *Variant
		}{e, v})
		record("assigned", fmt.Sprintf("%s %s: layer bucket %d, variant bucket %d → %s", diversion, unit, lb, vb, v.Key))
	}

	// Launched configs are the new defaults; running experiments override
	// them. Between experiments the one that started first wins (see
	// PriorityRules).
	sort.SliceStable(launched, func(i, j int) bool { return launched[i].e.LaunchOrder < launched[j].e.LaunchOrder })
	sort.SliceStable(assigned, func(i, j int) bool { return Before(assigned[i].e, assigned[j].e) })
	m := newMerger()
	for _, h := range launched {
		m.apply(h.e.ID, h.v.Params, true)
	}
	m.lockLaunches()
	for _, h := range assigned {
		m.apply(h.e.ID, h.v.Params, false)
	}
	res.Params = m.result()
	res.Conflicts = m.conflicts
	return res
}

// PriorityRules explains, in order, how conflicting parameters are resolved.
var PriorityRules = []string{
	"Experiments in the same layer never share a unit, so they never conflict.",
	"A running experiment (or a test-user assignment) overrides a fully launched variant's parameters.",
	"Between two experiments that set the same parameter, the one that started first wins; experiments not started yet come after all started ones.",
	"If they started at the same second, the smaller experiment id wins.",
	"Between two launched variants, the one launched most recently wins.",
}

// Before reports whether a takes priority over b when both set a parameter.
func Before(a, b *Experiment) bool {
	sa, sb := startKey(a), startKey(b)
	if sa != sb {
		return sa < sb
	}
	return a.ID < b.ID
}

func startKey(e *Experiment) int64 {
	if e.StartOrder <= 0 {
		return math.MaxInt64
	}
	return e.StartOrder
}

func whitelistable(status string) bool {
	switch status {
	case StatusDraft, StatusInReview, StatusApproved, StatusRejected, StatusActive, StatusPaused:
		return true
	}
	return false
}

func variantByID(e *Experiment, id int64) *Variant {
	for i := range e.Variants {
		if e.Variants[i].ID == id {
			return &e.Variants[i]
		}
	}
	return nil
}

// ---- targeting ----

// Match reports whether attrs satisfy the targeting, and if not, why.
func Match(t Targeting, attrs map[string]any) (bool, string) {
	if len(t.Groups) == 0 {
		return true, ""
	}
	var why []string
	for gi, g := range t.Groups {
		ok, reason := matchAll(g, attrs)
		if ok {
			return true, ""
		}
		if len(t.Groups) > 1 {
			reason = fmt.Sprintf("group %d: %s", gi+1, reason)
		}
		why = append(why, reason)
	}
	return false, strings.Join(why, "; ")
}

func matchAll(rules []Rule, attrs map[string]any) (bool, string) {
	for _, r := range rules {
		raw, present := attrs[r.Attr]
		val := ""
		if present {
			val = attrString(raw)
		}
		if !evalRule(r, val, present) {
			shown := "missing"
			if present {
				shown = strconv.Quote(val)
			}
			return false, fmt.Sprintf("%s %s %s failed (value %s)", r.Attr, r.Op, strings.Join(r.Values, ","), shown)
		}
	}
	return true, ""
}

func attrString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func evalRule(r Rule, val string, present bool) bool {
	first := ""
	if len(r.Values) > 0 {
		first = r.Values[0]
	}
	switch r.Op {
	case "exists":
		return present
	case "not_exists":
		return !present
	}
	if !present {
		// A missing attribute only satisfies negative rules.
		return r.Op == "neq" || r.Op == "not_in"
	}
	switch r.Op {
	case "eq":
		return strings.EqualFold(val, first)
	case "neq":
		return !strings.EqualFold(val, first)
	case "in", "not_in":
		in := false
		for _, v := range r.Values {
			if strings.EqualFold(val, v) {
				in = true
				break
			}
		}
		return in == (r.Op == "in")
	case "gt", "gte", "lt", "lte":
		a, err1 := strconv.ParseFloat(val, 64)
		b, err2 := strconv.ParseFloat(first, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch r.Op {
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		default:
			return a <= b
		}
	case "version_eq":
		return CompareVersions(val, first) == 0
	case "version_gt":
		return CompareVersions(val, first) > 0
	case "version_gte":
		return CompareVersions(val, first) >= 0
	case "version_lt":
		return CompareVersions(val, first) < 0
	case "version_lte":
		return CompareVersions(val, first) <= 0
	}
	return false
}

// CompareVersions compares dotted versions numerically ("10.2" > "9.9").
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ValidateTargeting checks targeting is well-formed. known, if not nil,
// lists the attributes rules may use.
func ValidateTargeting(t Targeting, known map[string]bool) error {
	if len(t.Groups) > 20 {
		return fmt.Errorf("targeting can have at most 20 OR groups")
	}
	for gi, g := range t.Groups {
		if len(g) == 0 {
			return fmt.Errorf("OR group %d is empty; add a condition or remove it", gi+1)
		}
		if len(g) > 20 {
			return fmt.Errorf("a group can have at most 20 conditions")
		}
		for _, r := range g {
			if r.Attr == "" || len(r.Attr) > 64 {
				return fmt.Errorf("every condition needs an attribute")
			}
			if known != nil && !known[r.Attr] {
				return fmt.Errorf("attribute %q isn't registered; add it under Targeting attributes first", r.Attr)
			}
			ok := false
			for _, op := range Ops {
				if op == r.Op {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("unknown targeting operator %q", r.Op)
			}
			if r.Op != "exists" && r.Op != "not_exists" && len(r.Values) == 0 {
				return fmt.Errorf("the condition on %q needs a value", r.Attr)
			}
		}
	}
	return nil
}

// Attrs lists the attributes the targeting uses.
func (t Targeting) Attrs() []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range t.Groups {
		for _, r := range g {
			if !seen[r.Attr] {
				seen[r.Attr] = true
				out = append(out, r.Attr)
			}
		}
	}
	return out
}

// ---- traffic allocation ----

// Allocate returns the buckets an experiment should hold to reach target
// buckets, given its current buckets and the buckets taken by others in the
// layer. Ramping up appends free buckets (chosen in a salt-dependent order so
// successive experiments don't all take the same ones); ramping down drops
// the most recently added, so every unit that stays keeps its assignment.
func Allocate(current []int, target int, taken map[int]bool, salt string) ([]int, error) {
	if target < 0 || target > Buckets {
		return nil, fmt.Errorf("traffic must be between 0 and %d buckets", Buckets)
	}
	if target <= len(current) {
		return append([]int(nil), current[:target]...), nil
	}
	mine := map[int]bool{}
	for _, b := range current {
		mine[b] = true
	}
	type cand struct{ b, order int }
	var free []cand
	for b := 0; b < Buckets; b++ {
		if !taken[b] && !mine[b] {
			free = append(free, cand{b, Hash(fmt.Sprintf("alloc:%s:%d", salt, b))*Buckets + b})
		}
	}
	need := target - len(current)
	if need > len(free) {
		return nil, fmt.Errorf("layer only has %.1f%% free traffic", float64(len(free))/10)
	}
	sort.Slice(free, func(i, j int) bool { return free[i].order < free[j].order })
	out := append([]int(nil), current...)
	for _, c := range free[:need] {
		out = append(out, c.b)
	}
	return out, nil
}

// ---- parameter merging ----

type leaf struct {
	value  any
	owner  int64
	launch bool
	locked bool // set by a launch before experiments are applied
}

type merger struct {
	leaves    map[string]*leaf
	conflicts []Conflict
}

func newMerger() *merger { return &merger{leaves: map[string]*leaf{}} }

func (m *merger) lockLaunches() {
	for _, l := range m.leaves {
		l.locked = true
	}
}

// apply writes params flattened to dot paths. Arrays are atomic values.
func (m *merger) apply(owner int64, params map[string]any, launch bool) {
	flat := map[string]any{}
	flatten("", params, flat)
	paths := make([]string, 0, len(flat))
	for p := range flat {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		clashes := m.clashes(p)
		blocked := false
		for _, c := range clashes {
			l := m.leaves[c]
			// Launch values are defaults: anything applied later replaces
			// them. A running experiment's value is never replaced.
			if !(l.launch && (launch || l.locked)) {
				m.conflicts = append(m.conflicts, Conflict{Path: p, Winner: l.owner, Loser: owner})
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		for _, c := range clashes {
			delete(m.leaves, c)
		}
		m.leaves[p] = &leaf{value: flat[p], owner: owner, launch: launch}
	}
}

// clashes lists existing paths equal to p, or nested under/above it.
func (m *merger) clashes(p string) []string {
	var out []string
	for q := range m.leaves {
		if q == p || strings.HasPrefix(q, p+".") || strings.HasPrefix(p, q+".") {
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
}

func (m *merger) result() map[string]any {
	root := map[string]any{}
	for p, l := range m.leaves {
		parts := strings.Split(p, ".")
		cur := root
		for i, k := range parts {
			if i == len(parts)-1 {
				cur[k] = l.value
				break
			}
			next, ok := cur[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[k] = next
			}
			cur = next
		}
	}
	return root
}

func flatten(prefix string, v any, out map[string]any) {
	obj, ok := v.(map[string]any)
	if !ok {
		if prefix != "" {
			out[prefix] = v
		}
		return
	}
	if len(obj) == 0 && prefix != "" {
		out[prefix] = obj
		return
	}
	for k, vv := range obj {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		flatten(p, vv, out)
	}
}

// ParamPaths lists the dot paths a params object sets (for parameter search).
func ParamPaths(params map[string]any) []string {
	flat := map[string]any{}
	flatten("", params, flat)
	out := make([]string, 0, len(flat))
	for p := range flat {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
