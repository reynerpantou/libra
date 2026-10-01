package tuning

import (
	"errors"
	"math"
)

// gp is a Gaussian-process regression over [0,1]^d with a Matérn 5/2
// kernel, a constant mean and per-observation (heteroscedastic) noise: each
// observation is a measured lift whose standard error we know.
type gp struct {
	x      [][]float64
	y      []float64
	mean   float64
	ls     float64 // length scale
	sf2    float64 // signal variance
	chol   [][]float64
	alpha  []float64
	noise  []float64
	logLik float64
}

func matern52(a, b []float64, ls, sf2 float64) float64 {
	var d2 float64
	for i := range a {
		t := a[i] - b[i]
		d2 += t * t
	}
	r := math.Sqrt(d2) / ls
	s5 := math.Sqrt(5) * r
	return sf2 * (1 + s5 + 5*r*r/3) * math.Exp(-s5)
}

// fitGP fits the hyperparameters by maximising the marginal likelihood
// over a small grid, which is robust for the few dozen points a study has.
func fitGP(x [][]float64, y, noiseVar []float64) (*gp, error) {
	n := len(x)
	if n == 0 {
		return nil, errors.New("no observations")
	}
	mean := 0.0
	for _, v := range y {
		mean += v
	}
	mean /= float64(n)
	vy := 0.0
	for _, v := range y {
		vy += (v - mean) * (v - mean)
	}
	if n > 1 {
		vy /= float64(n - 1)
	}
	avgNoise := 0.0
	for _, v := range noiseVar {
		avgNoise += v
	}
	avgNoise /= float64(n)
	base := math.Max(vy, avgNoise)
	if base <= 0 {
		base = 1e-4
	}
	var best *gp
	for _, ls := range []float64{0.08, 0.12, 0.18, 0.25, 0.35, 0.5, 0.7, 1.0, 1.5} {
		for _, f := range []float64{0.1, 0.3, 1, 3} {
			g, err := newGP(x, y, noiseVar, mean, ls, f*base)
			if err != nil {
				continue
			}
			if best == nil || g.logLik > best.logLik {
				best = g
			}
		}
	}
	if best == nil {
		return nil, errors.New("could not fit the model")
	}
	return best, nil
}

func newGP(x [][]float64, y, noiseVar []float64, mean, ls, sf2 float64) (*gp, error) {
	n := len(x)
	k := make([][]float64, n)
	for i := range k {
		k[i] = make([]float64, n)
		for j := 0; j <= i; j++ {
			k[i][j] = matern52(x[i], x[j], ls, sf2)
			k[j][i] = k[i][j]
		}
		k[i][i] += noiseVar[i] + 1e-9*sf2 + 1e-12
	}
	l, err := cholesky(k)
	if err != nil {
		return nil, err
	}
	r := make([]float64, n)
	for i := range y {
		r[i] = y[i] - mean
	}
	alpha := cholSolve(l, r)
	ll := 0.0
	for i := range r {
		ll -= 0.5 * r[i] * alpha[i]
		ll -= math.Log(l[i][i])
	}
	ll -= 0.5 * float64(n) * math.Log(2*math.Pi)
	return &gp{x: x, y: y, mean: mean, ls: ls, sf2: sf2, chol: l, alpha: alpha, noise: noiseVar, logLik: ll}, nil
}

// predict returns the posterior mean and standard deviation of the latent
// function (without observation noise) at p.
func (g *gp) predict(p []float64) (float64, float64) {
	n := len(g.x)
	ks := make([]float64, n)
	mu := g.mean
	for i := range g.x {
		ks[i] = matern52(p, g.x[i], g.ls, g.sf2)
		mu += ks[i] * g.alpha[i]
	}
	v := forwardSub(g.chol, ks)
	s2 := g.sf2
	for _, t := range v {
		s2 -= t * t
	}
	if s2 < 1e-12 {
		s2 = 1e-12
	}
	return mu, math.Sqrt(s2)
}

// with returns a model that also "observed" y at p with the given noise —
// the kriging-believer trick for picking a batch: pretend the chosen point
// returned its predicted mean, so the next pick goes elsewhere.
func (g *gp) with(p []float64, y, noise float64) *gp {
	x := append(append([][]float64{}, g.x...), p)
	ys := append(append([]float64{}, g.y...), y)
	noises := append(append([]float64{}, g.noise...), noise)
	ng, err := newGP(x, ys, noises, g.mean, g.ls, g.sf2)
	if err != nil {
		return g
	}
	return ng
}

func cholesky(a [][]float64) ([][]float64, error) {
	n := len(a)
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j <= i; j++ {
			s := a[i][j]
			for k := 0; k < j; k++ {
				s -= l[i][k] * l[j][k]
			}
			if i == j {
				if s <= 0 {
					return nil, errors.New("matrix is not positive definite")
				}
				l[i][i] = math.Sqrt(s)
			} else {
				l[i][j] = s / l[j][j]
			}
		}
	}
	return l, nil
}

func forwardSub(l [][]float64, b []float64) []float64 {
	n := len(b)
	y := make([]float64, n)
	for i := 0; i < n; i++ {
		s := b[i]
		for k := 0; k < i; k++ {
			s -= l[i][k] * y[k]
		}
		y[i] = s / l[i][i]
	}
	return y
}

func cholSolve(l [][]float64, b []float64) []float64 {
	y := forwardSub(l, b)
	n := len(y)
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for k := i + 1; k < n; k++ {
			s -= l[k][i] * x[k]
		}
		x[i] = s / l[i][i]
	}
	return x
}

func normPDF(z float64) float64 { return math.Exp(-0.5*z*z) / math.Sqrt(2*math.Pi) }
func normCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

// expectedImprovement over best for a maximisation problem.
func expectedImprovement(mu, sd, best float64) float64 {
	if sd <= 0 {
		return math.Max(0, mu-best)
	}
	z := (mu - best) / sd
	return (mu-best)*normCDF(z) + sd*normPDF(z)
}
