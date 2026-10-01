package tuning

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Algorithms.
const (
	Random      = "random"
	QuasiRandom = "quasi_random"
	Bayesian    = "bayesian"
	Constrained = "constrained"
)

// Algorithms lists the valid algorithm names.
var Algorithms = []string{Random, QuasiRandom, Bayesian, Constrained}

// Obs is one arm's result in one round.
type Obs struct {
	X     []float64 `json:"x"`     // the point, in unit space
	Y     float64   `json:"y"`     // objective lift vs v0, oriented so higher is better (0.02 = +2%)
	Var   float64   `json:"var"`   // its variance (standard error squared)
	Cons  []Measure `json:"cons"`  // per guardrail: slack = oriented lift + allowed drop (≥ 0 holds)
	Round int       `json:"round"` // the round it was measured in
}

// Measure is an estimate and its variance.
type Measure struct {
	Y   float64 `json:"y"`
	Var float64 `json:"var"`
}

// State is carried between rounds.
type State struct {
	HaltonIndex int     `json:"halton_index"` // next index of the quasi-random sequence
	Trust       float64 `json:"trust"`        // constrained: side of the trust region (unit space)
}

// Prediction is the model's view of a point.
type Prediction struct {
	Mean     float64 `json:"mean"`     // expected oriented lift
	SD       float64 `json:"sd"`       // its uncertainty
	Feasible float64 `json:"feasible"` // probability every guardrail holds (1 without guardrails)
}

// Candidate is a point for one arm.
type Candidate struct {
	X         []float64   `json:"x"`
	Values    []float64   `json:"values"`
	Source    string      `json:"source"` // how it was chosen
	Predicted *Prediction `json:"predicted,omitempty"`
}

// Best is the recommendation so far: the tested point the model rates
// highest among those likely to respect the guardrails.
type Best struct {
	Obs       int        `json:"obs"` // index into the observations
	X         []float64  `json:"x"`
	Values    []float64  `json:"values"`
	Predicted Prediction `json:"predicted"`
	Observed  float64    `json:"observed"`
}

// Request asks for the next round's candidates.
type Request struct {
	Algorithm   string
	Space       Space
	Arms        int
	Seed        int64
	Round       int // the round being proposed, from 1
	KeepBest    bool
	Guardrails  int
	Obs         []Obs
	State       State
	PoolSize    int // candidate pool for the acquisition (default 1500)
	MinObsModel int // observations needed before modelling (default max(4, d+2))
}

// Proposal is the next round's candidates.
type Proposal struct {
	Candidates []Candidate `json:"candidates"`
	State      State       `json:"state"`
	Best       *Best       `json:"best,omitempty"`
	Note       string      `json:"note"`
}

// models are fitted Gaussian processes for the objective and guardrails.
type models struct {
	obj  *gp
	cons []*gp
}

func fitModels(obs []Obs, guardrails int) *models {
	if len(obs) < 2 {
		return nil
	}
	x := make([][]float64, len(obs))
	y := make([]float64, len(obs))
	v := make([]float64, len(obs))
	for i, o := range obs {
		x[i], y[i], v[i] = o.X, o.Y, math.Max(o.Var, 1e-8)
	}
	g, err := fitGP(x, y, v)
	if err != nil {
		return nil
	}
	m := &models{obj: g}
	for c := 0; c < guardrails; c++ {
		cy := make([]float64, len(obs))
		cv := make([]float64, len(obs))
		for i, o := range obs {
			if c < len(o.Cons) {
				cy[i], cv[i] = o.Cons[c].Y, math.Max(o.Cons[c].Var, 1e-8)
			} else {
				cy[i], cv[i] = 0, 1
			}
		}
		cg, err := fitGP(x, cy, cv)
		if err != nil {
			return nil
		}
		m.cons = append(m.cons, cg)
	}
	return m
}

func (m *models) predict(p []float64) Prediction {
	mu, sd := m.obj.predict(p)
	pf := 1.0
	for _, c := range m.cons {
		cm, cs := c.predict(p)
		pf *= normCDF(cm / cs)
	}
	return Prediction{Mean: mu, SD: sd, Feasible: pf}
}

// FindBest picks the recommendation among the observed points.
func FindBest(sp Space, obs []Obs, guardrails int) *Best {
	if len(obs) == 0 {
		return nil
	}
	m := fitModels(obs, guardrails)
	best := -1
	var bp Prediction
	score := func(p Prediction) float64 {
		// Points unlikely to respect the guardrails rank after every
		// likely-feasible one.
		if p.Feasible < 0.5 {
			return -1e9 + p.Feasible
		}
		return p.Mean
	}
	for i, o := range obs {
		var p Prediction
		if m != nil {
			p = m.predict(o.X)
		} else {
			p = Prediction{Mean: o.Y, SD: math.Sqrt(o.Var), Feasible: observedFeasible(o)}
		}
		if best < 0 || score(p) > score(bp) {
			best, bp = i, p
		}
	}
	return &Best{Obs: best, X: obs[best].X, Values: sp.FromUnit(obs[best].X), Predicted: bp, Observed: obs[best].Y}
}

func observedFeasible(o Obs) float64 {
	p := 1.0
	for _, c := range o.Cons {
		p *= normCDF(c.Y / math.Sqrt(math.Max(c.Var, 1e-12)))
	}
	return p
}

// Propose returns the candidates for the next round.
func Propose(req Request) (Proposal, error) {
	if err := req.Space.Validate(); err != nil {
		return Proposal{}, err
	}
	if req.Arms < 1 || req.Arms > 20 {
		return Proposal{}, fmt.Errorf("1 to 20 arms")
	}
	d := len(req.Space)
	if req.PoolSize <= 0 {
		req.PoolSize = 1500
	}
	if req.MinObsModel <= 0 {
		req.MinObsModel = max(4, d+2)
	}
	st := req.State
	if st.HaltonIndex < 1 {
		st.HaltonIndex = 1 // index 0 is the corner of the space
	}
	if st.Trust <= 0 {
		st.Trust = 0.8
	}
	rng := rand.New(rand.NewSource(req.Seed*1_000_003 + int64(req.Round)))
	shift := haltonShift(req.Seed, d)
	out := Proposal{}
	var chosen []Candidate
	seen := map[string]bool{}
	add := func(x []float64, source string, pred *Prediction) bool {
		vals := req.Space.FromUnit(x)
		k := fmt.Sprint(vals)
		if seen[k] {
			return false
		}
		seen[k] = true
		chosen = append(chosen, Candidate{X: req.Space.ToUnit(vals), Values: vals, Source: source, Predicted: pred})
		return true
	}

	out.Best = FindBest(req.Space, req.Obs, req.Guardrails)
	if req.KeepBest && out.Best != nil && req.Arms > 1 {
		add(out.Best.X, "best_so_far", &out.Best.Predicted)
	}

	quasi := func(source string) {
		for tries := 0; len(chosen) < req.Arms && tries < req.Arms*50; tries++ {
			x := halton(st.HaltonIndex, d, shift)
			st.HaltonIndex++
			add(x, source, nil)
		}
	}
	uniform := func(source string) {
		for tries := 0; len(chosen) < req.Arms && tries < req.Arms*50; tries++ {
			x := make([]float64, d)
			for i := range x {
				x[i] = rng.Float64()
			}
			add(x, source, nil)
		}
	}

	switch req.Algorithm {
	case Random:
		uniform(Random)
		out.Note = "Uniform random points: pure exploration."
	case QuasiRandom:
		quasi(QuasiRandom)
		out.Note = "The next points of a scrambled Halton sequence: even coverage of the space."
	case Bayesian, Constrained:
		usable := 0
		for _, o := range req.Obs {
			if o.Var > 0 {
				usable++
			}
		}
		guardrails := 0
		if req.Algorithm == Constrained {
			guardrails = req.Guardrails // bayesian ignores guardrails when searching
		}
		var m *models
		if usable >= req.MinObsModel {
			m = fitModels(req.Obs, guardrails)
		}
		if m == nil {
			quasi("warm_up")
			out.Note = fmt.Sprintf("Warm-up: quasi-random points until there are %d results to model.", req.MinObsModel)
			break
		}
		constrained := req.Algorithm == Constrained
		center := make([]float64, d)
		for i := range center {
			center[i] = 0.5
		}
		if constrained {
			st.Trust = updateTrust(req, m, st.Trust)
			if b := FindBest(req.Space, req.Obs, req.Guardrails); b != nil {
				center = b.X
			}
		}
		pool := candidatePool(req, m, rng, shift, center, st.Trust, constrained)
		// The incumbent: the best expected lift among tested points (that
		// likely respect the guardrails).
		incumbent := math.Inf(-1)
		for _, o := range req.Obs {
			p := m.predict(o.X)
			if !constrained || p.Feasible >= 0.5 {
				incumbent = math.Max(incumbent, p.Mean)
			}
		}
		g := m.obj
		for len(chosen) < req.Arms {
			bestA, bestI := math.Inf(-1), -1
			for i, x := range pool {
				if x == nil {
					continue
				}
				mu, sd := g.predict(x)
				var a float64
				if math.IsInf(incumbent, -1) {
					a = 0 // nothing feasible yet: look for feasibility first
				} else {
					a = expectedImprovement(mu, sd, incumbent)
				}
				if constrained {
					pf := 1.0
					for _, c := range m.cons {
						cm, cs := c.predict(x)
						pf *= normCDF(cm / cs)
					}
					if math.IsInf(incumbent, -1) {
						a = pf
					} else {
						a *= pf
					}
				}
				// Tie-break toward uncertainty so a flat acquisition still
				// explores rather than repeating itself.
				a += 1e-9 * sd
				if a > bestA {
					bestA, bestI = a, i
				}
			}
			if bestI < 0 {
				break
			}
			x := pool[bestI]
			pool[bestI] = nil
			p := m.predict(x)
			if add(x, req.Algorithm, &p) {
				// Kriging believer: assume the pick returns its mean, so the
				// next pick spreads out.
				mu, _ := g.predict(x)
				g = g.with(x, mu, 1e-8)
			}
		}
		if len(chosen) < req.Arms {
			quasi(req.Algorithm)
		}
		if constrained {
			out.Note = fmt.Sprintf("Expected improvement × probability the guardrails hold, inside a trust region of side %.2f around the best feasible point.", st.Trust)
		} else {
			out.Note = "Expected improvement under a Gaussian-process model of every result so far, chosen as a batch."
		}
	default:
		return Proposal{}, fmt.Errorf("unknown algorithm %q", req.Algorithm)
	}
	out.Candidates = chosen
	out.State = st
	return out, nil
}

// updateTrust grows the trust region after a round that improved on the
// incumbent and shrinks it otherwise.
func updateTrust(req Request, m *models, trust float64) float64 {
	last := req.Round - 1
	before, after := math.Inf(-1), math.Inf(-1)
	any := false
	for _, o := range req.Obs {
		p := m.predict(o.X)
		if p.Feasible < 0.5 {
			continue
		}
		if o.Round < last {
			before = math.Max(before, p.Mean)
		} else if o.Round == last {
			after = math.Max(after, p.Mean)
			any = true
		}
	}
	if !any || math.IsInf(before, -1) {
		return trust
	}
	if after > before+0.001 {
		return math.Min(1, trust*1.5)
	}
	return math.Max(0.1, trust*0.6)
}

// candidatePool is where the acquisition looks: quasi-random points over
// the space (or the trust region), plus local perturbations around the
// best tested points.
func candidatePool(req Request, m *models, rng *rand.Rand, shift []float64, center []float64, trust float64, local bool) [][]float64 {
	d := len(req.Space)
	pool := make([][]float64, 0, req.PoolSize+200)
	start := 10_000 + req.Round*req.PoolSize // a different stretch of the sequence each round
	for i := 0; i < req.PoolSize; i++ {
		u := halton(start+i, d, shift)
		if local {
			for j := range u {
				u[j] = clamp01(center[j] + (u[j]-0.5)*trust)
			}
		}
		pool = append(pool, u)
	}
	// Refine around the best few tested points.
	type ranked struct {
		x []float64
		m float64
	}
	var rs []ranked
	for _, o := range req.Obs {
		p := m.predict(o.X)
		rs = append(rs, ranked{o.X, p.Mean})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].m > rs[j].m })
	sd := 0.08
	if local {
		sd = 0.08 * trust
	}
	for k := 0; k < len(rs) && k < 5; k++ {
		for n := 0; n < 40; n++ {
			u := make([]float64, d)
			for j := range u {
				u[j] = clamp01(rs[k].x[j] + sd*rng.NormFloat64())
				if local {
					u[j] = math.Max(center[j]-trust/2, math.Min(center[j]+trust/2, u[j]))
					u[j] = clamp01(u[j])
				}
			}
			pool = append(pool, u)
		}
	}
	return pool
}

var primes = []int{2, 3, 5, 7, 11, 13, 17, 19, 23, 29}

// halton is point i of a Halton sequence, rotated by shift (Cranley–
// Patterson) so each study gets its own scrambled sequence.
func halton(i, d int, shift []float64) []float64 {
	u := make([]float64, d)
	for j := 0; j < d; j++ {
		b := primes[j%len(primes)]
		f, r := 1.0, 0.0
		for n := i; n > 0; n /= b {
			f /= float64(b)
			r += f * float64(n%b)
		}
		u[j] = math.Mod(r+shift[j], 1)
	}
	return u
}

func haltonShift(seed int64, d int) []float64 {
	r := rand.New(rand.NewSource(seed))
	s := make([]float64, d)
	for i := range s {
		s[i] = r.Float64()
	}
	return s
}

// SurfacePoint is the model's view of one grid point.
type SurfacePoint struct {
	X float64 `json:"x"` // real value of the first parameter
	Y float64 `json:"y"` // real value of the second (0 for a 1-D curve)
	Prediction
}

// Surface predicts expected lift on an n×n grid over parameters xi and yi
// (yi < 0: a curve over xi alone), holding the others at fixed. It returns
// nil until there are enough results to model.
func Surface(sp Space, obs []Obs, guardrails, xi, yi int, fixed []float64, n int) []SurfacePoint {
	m := fitModels(obs, guardrails)
	if m == nil || n < 2 {
		return nil
	}
	base := sp.ToUnit(fixed)
	var out []SurfacePoint
	ny := n
	if yi < 0 {
		ny = 1
	}
	for j := 0; j < ny; j++ {
		for i := 0; i < n; i++ {
			u := append([]float64{}, base...)
			u[xi] = float64(i) / float64(n-1)
			if yi >= 0 {
				u[yi] = float64(j) / float64(n-1)
			}
			vals := sp.FromUnit(u)
			pt := SurfacePoint{X: vals[xi], Prediction: m.predict(u)}
			if yi >= 0 {
				pt.Y = vals[yi]
			}
			out = append(out, pt)
		}
	}
	return out
}
