package assign

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

func twoVariants() []Variant {
	return []Variant{
		{ID: 1, Key: "control", IsControl: true, Weight: 500, Params: map[string]any{"rank": map[string]any{"formula": "old"}}},
		{ID: 2, Key: "treatment", Weight: 500, Params: map[string]any{"rank": map[string]any{"formula": "new"}}},
	}
}

func buckets(n int) []int {
	out, _ := Allocate(nil, n, nil, "x")
	return out
}

func TestHashStableAndUniform(t *testing.T) {
	if Hash("abc") != Hash("abc") {
		t.Fatal("hash not deterministic")
	}
	counts := make([]int, 10)
	for i := 0; i < 100000; i++ {
		counts[Hash(fmt.Sprint("u", i))/100]++
	}
	for i, c := range counts {
		if c < 9500 || c > 10500 {
			t.Errorf("decile %d has %d", i, c)
		}
	}
}

func TestSplitAndStickiness(t *testing.T) {
	e := &Experiment{ID: 1, LayerID: 1, Name: "e", Status: StatusActive, Salt: "s1", Buckets: buckets(500), Variants: twoVariants()}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "L"}}, []*Experiment{e})
	in, treat := 0, 0
	first := map[string]string{}
	for i := 0; i < 20000; i++ {
		u := fmt.Sprint("user", i)
		r := s.Resolve(Request{UnitID: u}, false)
		if len(r.Hits) == 1 {
			in++
			first[u] = r.Hits[0].Variant
			if r.Hits[0].Variant == "treatment" {
				treat++
			}
		}
	}
	if math.Abs(float64(in)/20000-0.5) > 0.02 {
		t.Errorf("traffic %d/20000, want ~50%%", in)
	}
	if math.Abs(float64(treat)/float64(in)-0.5) > 0.02 {
		t.Errorf("treatment share %d/%d", treat, in)
	}

	// Ramp to 80%: everyone already in stays, with the same variant.
	grown, err := Allocate(e.Buckets, 800, nil, e.Salt)
	if err != nil {
		t.Fatal(err)
	}
	e2 := *e
	e2.Buckets = grown
	s2 := NewSnapshot(2, []*Layer{{ID: 1, Salt: "L"}}, []*Experiment{&e2})
	for u, v := range first {
		r := s2.Resolve(Request{UnitID: u}, false)
		if len(r.Hits) != 1 || r.Hits[0].Variant != v {
			t.Fatalf("%s moved from %s after ramp-up: %+v", u, v, r.Hits)
		}
	}
}

func TestLayerMutualExclusion(t *testing.T) {
	a := &Experiment{ID: 1, LayerID: 1, Name: "a", Status: StatusActive, Salt: "a", Variants: twoVariants()}
	b := &Experiment{ID: 2, LayerID: 1, Name: "b", Status: StatusActive, Salt: "b", Variants: twoVariants()}
	a.Buckets, _ = Allocate(nil, 400, nil, a.Salt)
	taken := map[int]bool{}
	for _, x := range a.Buckets {
		taken[x] = true
	}
	b.Buckets, _ = Allocate(nil, 600, taken, b.Salt)
	if _, err := Allocate(nil, 1, map[int]bool{}, "c"); err != nil {
		t.Fatal(err)
	}
	all := map[int]bool{}
	for _, x := range append(append([]int{}, a.Buckets...), b.Buckets...) {
		all[x] = true
	}
	if _, err := Allocate(nil, 1, all, "c"); err == nil {
		t.Fatal("expected full layer error")
	}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "L"}}, []*Experiment{a, b})
	for i := 0; i < 5000; i++ {
		r := s.Resolve(Request{UnitID: fmt.Sprint(i)}, false)
		if len(r.Hits) != 1 {
			t.Fatalf("unit %d hit %d experiments in one fully allocated layer", i, len(r.Hits))
		}
	}
}

func TestRampDownKeepsEarliest(t *testing.T) {
	cur := buckets(300)
	down, _ := Allocate(cur, 100, nil, "x")
	for i := range down {
		if down[i] != cur[i] {
			t.Fatal("ramp down must keep the earliest buckets")
		}
	}
}

func TestTargeting(t *testing.T) {
	rules := []Rule{
		{Attr: "region", Op: "in", Values: []string{"ID", "SG"}},
		{Attr: "app_version", Op: "version_gte", Values: []string{"10.2"}},
		{Attr: "age", Op: "gte", Values: []string{"18"}},
	}
	all := Targeting{Groups: [][]Rule{rules}}
	ok, _ := Match(all, map[string]any{"region": "id", "app_version": "10.10.1", "age": 20.0})
	if !ok {
		t.Error("expected match")
	}
	ok, why := Match(all, map[string]any{"region": "US", "app_version": "10.10.1", "age": 20.0})
	if ok || why == "" {
		t.Error("region should fail")
	}
	ok, _ = Match(all, map[string]any{"region": "SG", "app_version": "9.9", "age": 20.0})
	if ok {
		t.Error("version should fail")
	}
	ok, _ = Match(Targeting{Groups: [][]Rule{{{Attr: "os", Op: "neq", Values: []string{"ios"}}}}}, nil)
	if !ok {
		t.Error("missing attribute satisfies neq")
	}
}

func TestWhitelistLaunchAndMerge(t *testing.T) {
	launched := &Experiment{ID: 1, LayerID: 1, Name: "old", Status: StatusLaunched, LaunchedVar: 2, Variants: twoVariants()}
	running := &Experiment{ID: 2, LayerID: 2, Name: "new", Status: StatusActive, Salt: "r", StartOrder: 1000, Buckets: buckets(1000), Variants: []Variant{
		{ID: 3, Key: "control", IsControl: true, Weight: 500, Params: map[string]any{"rank": map[string]any{"boost": 1.0}}},
		{ID: 4, Key: "treatment", Weight: 500, Params: map[string]any{"rank": map[string]any{"formula": "exp", "boost": 1.5}}},
	}}
	draft := &Experiment{ID: 3, LayerID: 3, Name: "qa", Status: StatusDraft, Salt: "q", Variants: []Variant{
		{ID: 5, Key: "control", Weight: 500, Params: map[string]any{"ui": "a"}},
		{ID: 6, Key: "treatment", Weight: 500, Params: map[string]any{"ui": "b", "rank": map[string]any{"formula": "qa"}}},
	}, Whitelist: map[string]int64{"tester": 6}}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "1"}, {ID: 2, Salt: "2"}, {ID: 3, Salt: "3"}}, []*Experiment{launched, running, draft})

	r := s.Resolve(Request{UnitID: "tester"}, true)
	if len(r.Hits) != 3 {
		t.Fatalf("hits %+v", r.Hits)
	}
	rank := r.Params["rank"].(map[string]any)
	if r.Params["ui"] != "b" {
		t.Errorf("whitelist params missing: %v", r.Params)
	}
	// Running experiment (id 2) is older than the draft (id 3): if it sets
	// rank.formula it wins; the launch default is overridden either way.
	if rank["formula"] == "new" {
		t.Errorf("launch default should be overridden: %v", rank)
	}
	if len(r.Trace) != 3 {
		t.Errorf("trace %+v", r.Trace)
	}

	r = s.Resolve(Request{UnitID: "someone"}, false)
	if len(r.Hits) != 2 {
		t.Fatalf("hits %+v", r.Hits)
	}
}

func TestMergerConflicts(t *testing.T) {
	m := newMerger()
	m.apply(1, map[string]any{"a": map[string]any{"b": 1}}, true)
	m.lockLaunches()
	m.apply(2, map[string]any{"a": 5}, false) // replaces launch subtree
	m.apply(3, map[string]any{"a": map[string]any{"c": 2}}, false)
	out := m.result()
	if out["a"] != 5 {
		t.Errorf("result %v", out)
	}
	if len(m.conflicts) != 1 || m.conflicts[0].Winner != 2 || m.conflicts[0].Loser != 3 {
		t.Errorf("conflicts %+v", m.conflicts)
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("10.2", "9.9") != 1 || CompareVersions("1.0", "1") != 0 || CompareVersions("1.2.3", "1.10") != -1 {
		t.Error("version compare")
	}
}

func TestTargetingOrGroups(t *testing.T) {
	// (device = android AND app_version >= 3.400) OR (device = ios AND app_version >= 2.300) OR device in (desktop, mobile)
	tg := Targeting{Groups: [][]Rule{
		{{Attr: "device", Op: "eq", Values: []string{"android"}}, {Attr: "app_version", Op: "version_gte", Values: []string{"3.400"}}},
		{{Attr: "device", Op: "eq", Values: []string{"ios"}}, {Attr: "app_version", Op: "version_gte", Values: []string{"2.300"}}},
		{{Attr: "device", Op: "in", Values: []string{"desktop", "mobile"}}},
	}}
	cases := []struct {
		attrs map[string]any
		want  bool
	}{
		{map[string]any{"device": "android", "app_version": "3.500"}, true},
		{map[string]any{"device": "android", "app_version": "2.500"}, false},
		{map[string]any{"device": "ios", "app_version": "2.300"}, true},
		{map[string]any{"device": "desktop"}, true},
		{map[string]any{"device": "tv"}, false},
		{map[string]any{}, false},
	}
	for _, c := range cases {
		if got, why := Match(tg, c.attrs); got != c.want {
			t.Errorf("%v: got %v (%s)", c.attrs, got, why)
		}
	}
	if ok, _ := Match(Targeting{}, nil); !ok {
		t.Error("empty targeting matches everyone")
	}
}

func TestTargetingJSON(t *testing.T) {
	var legacy Targeting
	if err := json.Unmarshal([]byte(`[{"attr":"region","op":"eq","values":["ID"]}]`), &legacy); err != nil || len(legacy.Groups) != 1 {
		t.Fatalf("legacy: %+v %v", legacy, err)
	}
	var empty Targeting
	_ = json.Unmarshal([]byte(`[]`), &empty)
	b, _ := json.Marshal(empty)
	if string(b) != `{"groups":[]}` {
		t.Errorf("empty marshals to %s", b)
	}
	if err := ValidateTargeting(Targeting{Groups: [][]Rule{{}}}, nil); err == nil {
		t.Error("empty group should be invalid")
	}
	if err := ValidateTargeting(legacy, map[string]bool{"os": true}); err == nil {
		t.Error("unregistered attribute should be invalid")
	}
}

func TestDeviceDiversion(t *testing.T) {
	e := &Experiment{ID: 1, LayerID: 1, Name: "d", Status: StatusActive, Salt: "s", Buckets: buckets(1000), Variants: twoVariants(),
		Whitelist: map[string]int64{"dev-qa": 2}}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "L", Diversion: DiversionDevice}}, []*Experiment{e})
	r := s.Resolve(Request{UserID: "u1"}, true)
	if len(r.Hits) != 0 || r.Trace[0].Outcome != "missing_id" {
		t.Fatalf("no device id: %+v", r)
	}
	a := s.Resolve(Request{UserID: "u1", DeviceID: "dev-7"}, false)
	b := s.Resolve(Request{UserID: "u2", DeviceID: "dev-7"}, false)
	if len(a.Hits) != 1 || a.Hits[0].UnitType != DiversionDevice || a.Hits[0].UnitID != "dev-7" || a.Hits[0].Variant != b.Hits[0].Variant {
		t.Errorf("device assignment should follow the device: %+v %+v", a.Hits, b.Hits)
	}
	if w := s.Resolve(Request{DeviceID: "dev-qa"}, false); w.Hits[0].Reason != ReasonTestUser {
		t.Errorf("device whitelist: %+v", w.Hits)
	}
}

func TestPriorityByStartTime(t *testing.T) {
	param := func(v string) []Variant {
		return []Variant{{ID: 1, Key: "only", IsControl: true, Weight: 1000, Params: map[string]any{"x": v}}}
	}
	// The higher id started first, so it wins despite the larger id.
	older := &Experiment{ID: 900, LayerID: 1, Name: "older", Status: StatusActive, Salt: "a", Buckets: buckets(1000), Variants: param("older"), StartOrder: 100}
	newer := &Experiment{ID: 100, LayerID: 2, Name: "newer", Status: StatusActive, Salt: "b", Buckets: buckets(1000), Variants: param("newer"), StartOrder: 200}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "1"}, {ID: 2, Salt: "2"}}, []*Experiment{older, newer})
	r := s.Resolve(Request{UserID: "u"}, false)
	if r.Params["x"] != "older" || len(r.Conflicts) != 1 || r.Conflicts[0].Winner != 900 {
		t.Errorf("params %v conflicts %+v", r.Params, r.Conflicts)
	}
}

func TestGradualLaunch(t *testing.T) {
	mk := func(rollout int) *Snapshot {
		e := &Experiment{ID: 1, LayerID: 1, Name: "launch", Status: StatusLaunched, Salt: "L", LaunchedVar: 2, LaunchRollout: rollout, Variants: twoVariants()}
		return NewSnapshot(1, []*Layer{{ID: 1, Salt: "1"}}, []*Experiment{e})
	}
	count := func(s *Snapshot) (in []string) {
		for i := 0; i < 4000; i++ {
			u := fmt.Sprintf("u%d", i)
			if len(s.Resolve(Request{UserID: u}, false).Hits) == 1 {
				in = append(in, u)
			}
		}
		return in
	}
	at30 := count(mk(300))
	if n := len(at30); n < 1000 || n > 1400 {
		t.Errorf("30%% rollout reached %d of 4000", n)
	}
	// Raising the share keeps everyone already in.
	at60 := map[string]bool{}
	for _, u := range count(mk(600)) {
		at60[u] = true
	}
	for _, u := range at30 {
		if !at60[u] {
			t.Fatalf("%s dropped out when the rollout grew", u)
		}
	}
	if n := len(count(mk(0))); n != 4000 {
		t.Errorf("unset rollout means everyone, got %d", n)
	}
	r := mk(300).Resolve(Request{}, true)
	if len(r.Hits) != 0 || r.Trace[0].Outcome != "missing_id" {
		t.Errorf("partial launch without an id: %+v", r)
	}
}

func TestPlatformScope(t *testing.T) {
	tiktok := &Experiment{ID: 1, LayerID: 1, Name: "tt", PlatformKey: "tiktok", BusinessKey: "search", Status: StatusLaunched, LaunchedVar: 2, Variants: twoVariants()}
	toko := &Experiment{ID: 2, LayerID: 1, Name: "tk", PlatformKey: "toko", BusinessKey: "search", Status: StatusLaunched, LaunchedVar: 2, Variants: twoVariants()}
	ads := &Experiment{ID: 3, LayerID: 1, Name: "ads", PlatformKey: "tiktok", BusinessKey: "ads", Status: StatusLaunched, LaunchedVar: 2, Variants: twoVariants()}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "1"}}, []*Experiment{tiktok, toko, ads})
	s.Platforms = map[string][]string{"tiktok": {"ads", "search"}, "toko": {"search"}}

	ids := func(sc map[string][]string) []int64 {
		var out []int64
		for _, h := range s.Resolve(Request{UserID: "u", Scope: sc}, false).Hits {
			out = append(out, h.ExperimentID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	for _, c := range []struct {
		scope map[string][]string
		want  []int64
	}{
		{nil, []int64{1, 2, 3}},                               // everything
		{map[string][]string{"tiktok": {}}, []int64{1, 3}},    // a whole platform
		{map[string][]string{"toko": {"search"}}, []int64{2}}, // "search" on both platforms: toko's only
		// Per platform: all of tiktok, but only toko's search.
		{map[string][]string{"tiktok": {"ads"}, "toko": {}}, []int64{2, 3}},
	} {
		if got := ids(c.scope); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("scope %v: got %v, want %v", c.scope, got, c.want)
		}
	}
	for sc, want := range map[string]map[string][]string{
		"unknown platform":          {"nope": nil},
		"has no business":           {"toko": {"ads"}},
		"(platforms: tiktok, toko)": {"x": nil},
	} {
		if msg := s.CheckScope(want); !strings.Contains(msg, sc) {
			t.Errorf("CheckScope(%v) = %q, want %q", want, msg, sc)
		}
	}
	if msg := s.CheckScope(map[string][]string{"tiktok": {"ads", "search"}}); msg != "" {
		t.Errorf("valid scope rejected: %s", msg)
	}
}

// A fully launched variant applies to every unit that passes targeting,
// whatever ids the request carries (or none), and whatever its layer
// splits by.
func TestLaunchAppliesToEveryone(t *testing.T) {
	e := &Experiment{ID: 1, LayerID: 1, Name: "launch", PlatformKey: "p", BusinessKey: "b", Status: StatusLaunched, Salt: "L", LaunchedVar: 2, LaunchRollout: 1000, Variants: twoVariants()}
	s := NewSnapshot(1, []*Layer{{ID: 1, Salt: "1", Diversion: "shop_id"}}, []*Experiment{e})
	for _, req := range []Request{{}, {UserID: "u1"}, {DeviceID: "d1"}, {IDs: map[string]string{"other": "x"}}} {
		r := s.Resolve(req, false)
		if len(r.Hits) != 1 || r.Hits[0].Reason != ReasonLaunched {
			t.Errorf("request %+v: hits %+v", req, r.Hits)
		}
	}
}

// An empty object (a control that keeps the defaults) sets nothing, so it
// never conflicts with another experiment's fields under it.
func TestEmptyObjectSetsNothing(t *testing.T) {
	if p := FlattenParams(map[string]any{"shop": map[string]any{}}); len(p) != 0 {
		t.Fatalf("empty object flattened to %v", p)
	}
	m := newMerger()
	m.apply(1, map[string]any{"shop": map[string]any{}}, false)
	m.apply(2, map[string]any{"shop": map[string]any{"recom": map[string]any{"v": "1"}}}, false)
	if len(m.conflicts) != 0 {
		t.Fatalf("unexpected conflicts %v", m.conflicts)
	}
	got := m.result()["shop"].(map[string]any)["recom"].(map[string]any)["v"]
	if got != "1" {
		t.Fatalf("value lost: %v", m.result())
	}
}
