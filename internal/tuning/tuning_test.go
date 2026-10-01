package tuning

import (
	"math"
	"math/rand"
	"testing"
)

var space2 = Space{
	{Path: "search.ranking.relevance_weight", Min: 0.2, Max: 1.0, Control: 0.5},
	{Path: "search.ranking.freshness_weight", Min: 0.2, Max: 1.0, Control: 0.5},
}

// truth is a smooth lift surface with its peak at (0.72, 0.62); the
// guardrail holds while freshness ≤ 0.5.
func truth(v []float64) float64 {
	return 0.12*math.Exp(-(math.Pow(v[0]-0.72, 2)/0.03+math.Pow(v[1]-0.62, 2)/0.05)) - 0.03
}
func guard(v []float64) float64 { return 0.5 - v[1] }

func run(t *testing.T, alg string, seed int64, rounds int) (best []float64, obs []Obs) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed + 99))
	st := State{}
	guardrails := 0
	if alg == Constrained {
		guardrails = 1
	}
	for r := 1; r <= rounds; r++ {
		p, err := Propose(Request{Algorithm: alg, Space: space2, Arms: 10, Seed: seed, Round: r, KeepBest: true, Guardrails: guardrails, Obs: obs, State: st, PoolSize: 600})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Candidates) != 10 {
			t.Fatalf("%s round %d: %d candidates", alg, r, len(p.Candidates))
		}
		st = p.State
		for _, c := range p.Candidates {
			for i, v := range c.Values {
				if v < space2[i].Min || v > space2[i].Max {
					t.Fatalf("value %v out of bounds", v)
				}
			}
			o := Obs{X: c.X, Y: truth(c.Values) + 0.01*rng.NormFloat64(), Var: 0.0001, Round: r}
			if guardrails > 0 {
				o.Cons = []Measure{{Y: guard(c.Values) + 0.01*rng.NormFloat64(), Var: 0.0001}}
			}
			obs = append(obs, o)
		}
	}
	b := FindBest(space2, obs, guardrails)
	return b.Values, obs
}

func TestAlgorithms(t *testing.T) {
	score := map[string]float64{}
	for _, alg := range Algorithms {
		for seed := int64(1); seed <= 4; seed++ {
			best, _ := run(t, alg, seed, 6)
			score[alg] += truth(best) / 4
			if alg == Constrained && guard(best) < -0.05 {
				t.Errorf("constrained seed %d recommended an infeasible point %v", seed, best)
			}
		}
	}
	t.Logf("mean true lift of the recommendation: %v", score)
	if score[Bayesian] < score[Random] {
		t.Errorf("bayesian (%.4f) should beat random (%.4f)", score[Bayesian], score[Random])
	}
	if score[Bayesian] < 0.06 {
		t.Errorf("bayesian should get near the peak (0.09), got %.4f", score[Bayesian])
	}
}

func TestQuasiRandomCoversSpace(t *testing.T) {
	st := State{}
	var pts [][]float64
	for r := 1; r <= 3; r++ {
		p, err := Propose(Request{Algorithm: QuasiRandom, Space: space2, Arms: 10, Seed: 7, Round: r, State: st})
		if err != nil {
			t.Fatal(err)
		}
		st = p.State
		for _, c := range p.Candidates {
			pts = append(pts, c.X)
		}
	}
	// Every quadrant gets points.
	q := map[[2]bool]int{}
	for _, x := range pts {
		q[[2]bool{x[0] < 0.5, x[1] < 0.5}]++
	}
	if len(q) != 4 {
		t.Fatalf("quadrants covered: %v", q)
	}
	if st.HaltonIndex != 31 {
		t.Fatalf("halton index %d", st.HaltonIndex)
	}
}

func TestSpace(t *testing.T) {
	sp := Space{
		{Path: "a.n", Type: "int", Min: 1, Max: 10, Control: 3},
		{Path: "a.w", Min: 0.001, Max: 1, Scale: "log", Control: 0.1},
		{Path: "b", Min: 0, Max: 1, Step: 0.25, Control: 0.5},
	}
	if err := sp.Validate(); err != nil {
		t.Fatal(err)
	}
	v := sp.FromUnit([]float64{0.5, 0.5, 0.6})
	if v[0] != 6 && v[0] != 5 {
		t.Fatalf("int %v", v[0])
	}
	if math.Abs(v[1]-math.Sqrt(0.001)) > 1e-4 {
		t.Fatalf("log midpoint %v", v[1])
	}
	if v[2] != 0.5 {
		t.Fatalf("step %v", v[2])
	}
	out := sp.Apply(map[string]any{"a": map[string]any{"keep": true}}, v)
	a := out["a"].(map[string]any)
	if a["keep"] != true || a["n"].(int64) != int64(v[0]) {
		t.Fatalf("apply %v", out)
	}
	bad := Space{{Path: "x", Min: 0, Max: 1}, {Path: "x.y", Min: 0, Max: 1}}
	if bad.Validate() == nil {
		t.Fatal("overlapping paths accepted")
	}
}
