package assign

import (
	"fmt"
	"math"
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
	ok, _ := Match(rules, map[string]any{"region": "id", "app_version": "10.10.1", "age": 20.0})
	if !ok {
		t.Error("expected match")
	}
	ok, why := Match(rules, map[string]any{"region": "US", "app_version": "10.10.1", "age": 20.0})
	if ok || why == "" {
		t.Error("region should fail")
	}
	ok, _ = Match(rules, map[string]any{"region": "SG", "app_version": "9.9", "age": 20.0})
	if ok {
		t.Error("version should fail")
	}
	ok, _ = Match([]Rule{{Attr: "os", Op: "neq", Values: []string{"ios"}}}, nil)
	if !ok {
		t.Error("missing attribute satisfies neq")
	}
}

func TestWhitelistLaunchAndMerge(t *testing.T) {
	launched := &Experiment{ID: 1, LayerID: 1, Name: "old", Status: StatusLaunched, LaunchedVar: 2, Variants: twoVariants()}
	running := &Experiment{ID: 2, LayerID: 2, Name: "new", Status: StatusActive, Salt: "r", Buckets: buckets(1000), Variants: []Variant{
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
