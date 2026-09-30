// Package stats holds the statistics behind experiment reports: the
// delta-method comparison of any formula metric between two variants, and the
// sample ratio mismatch (SRM) check on assignment counts.
package stats

import (
	"math"

	"github.com/reynerpantou/libra/internal/formula"
)

// Moments are the per-unit sufficient statistics of one variant: the unit
// count, and for k measures the sums Σx_i and cross sums Σx_i·x_j. They're all
// the delta method needs, and SQL can compute them in one pass.
type Moments struct {
	N     float64
	Sum   []float64   // k
	Cross [][]float64 // k×k, symmetric
}

// Mean returns the per-unit mean of each measure.
func (m Moments) Mean() []float64 {
	out := make([]float64, len(m.Sum))
	if m.N == 0 {
		return out
	}
	for i, s := range m.Sum {
		out[i] = s / m.N
	}
	return out
}

// Cov returns the sample covariance matrix of the measures.
func (m Moments) Cov() [][]float64 {
	k := len(m.Sum)
	out := make([][]float64, k)
	mean := m.Mean()
	for i := 0; i < k; i++ {
		out[i] = make([]float64, k)
		if m.N < 2 {
			continue
		}
		for j := 0; j < k; j++ {
			out[i][j] = (m.Cross[i][j] - m.N*mean[i]*mean[j]) / (m.N - 1)
		}
	}
	return out
}

// Estimate is a formula evaluated on one variant.
type Estimate struct {
	N        float64 `json:"n"`
	Value    float64 `json:"value"`     // the formula on per-unit means (what's compared)
	Total    float64 `json:"total"`     // the formula on totals (what a "total" metric shows)
	Variance float64 `json:"variance"`  // variance of Value (delta method)
	StdErr   float64 `json:"std_error"` // sqrt(Variance)
}

// Estimate evaluates node on per-unit means and propagates the covariance of
// those means through the formula's gradient (the delta method):
// Var(f(x̄)) ≈ ∇f(x̄)ᵀ Σ ∇f(x̄) / n.
func EstimateOf(node formula.Node, index map[string]int, m Moments) Estimate {
	mean := m.Mean()
	value, grad := formula.EvalGrad(node, mean, index)
	totals := make([]float64, len(m.Sum))
	copy(totals, m.Sum)
	total, _ := formula.EvalGrad(node, totals, index)

	e := Estimate{N: m.N, Value: value, Total: total, Variance: math.NaN(), StdErr: math.NaN()}
	if m.N < 2 || math.IsNaN(value) {
		return e
	}
	cov := m.Cov()
	v := 0.0
	for i := range grad {
		for j := range grad {
			v += grad[i] * cov[i][j] * grad[j]
		}
	}
	v /= m.N
	if v < 0 { // rounding on near-constant measures
		v = 0
	}
	e.Variance, e.StdErr = v, math.Sqrt(v)
	return e
}

// Comparison is treatment versus control for one metric.
type Comparison struct {
	AbsDiff     float64 `json:"abs_diff"`
	AbsCILow    float64 `json:"abs_ci_low"`
	AbsCIHigh   float64 `json:"abs_ci_high"`
	RelDiff     float64 `json:"rel_diff"` // (t - c) / c
	RelCILow    float64 `json:"rel_ci_low"`
	RelCIHigh   float64 `json:"rel_ci_high"`
	PValue      float64 `json:"p_value"`
	Significant bool    `json:"significant"`
	Testable    bool    `json:"testable"` // false when there's too little data for a test
}

// MinUnitsForTest is the smallest variant size a significance test is run on.
const MinUnitsForTest = 30

// Compare runs a two-sided z-test on the difference of two estimates at the
// given significance level (e.g. 0.05).
func Compare(treatment, control Estimate, alpha float64) Comparison {
	c := Comparison{
		AbsDiff: treatment.Value - control.Value,
		PValue:  math.NaN(), AbsCILow: math.NaN(), AbsCIHigh: math.NaN(),
		RelDiff: math.NaN(), RelCILow: math.NaN(), RelCIHigh: math.NaN(),
	}
	if control.Value != 0 {
		c.RelDiff = c.AbsDiff / control.Value
	}
	if treatment.N < MinUnitsForTest || control.N < MinUnitsForTest ||
		math.IsNaN(treatment.Variance) || math.IsNaN(control.Variance) || math.IsNaN(c.AbsDiff) {
		return c
	}
	z := NormalQuantile(1 - alpha/2)
	se := math.Sqrt(treatment.Variance + control.Variance)
	c.AbsCILow, c.AbsCIHigh = c.AbsDiff-z*se, c.AbsDiff+z*se
	if se == 0 {
		if c.AbsDiff == 0 {
			c.PValue = 1
		} else {
			c.PValue = 0
		}
	} else {
		c.PValue = 2 * (1 - NormalCDF(math.Abs(c.AbsDiff)/se))
	}
	c.Testable = true
	c.Significant = c.PValue < alpha
	if control.Value != 0 {
		// Delta method for the ratio t/c − 1 of two independent estimates.
		t, k := treatment.Value, control.Value
		relVar := treatment.Variance/(k*k) + t*t*control.Variance/(k*k*k*k)
		rs := math.Sqrt(relVar)
		c.RelCILow, c.RelCIHigh = c.RelDiff-z*rs, c.RelDiff+z*rs
	}
	return c
}

// SRM is the result of a sample ratio mismatch check.
type SRM struct {
	ChiSquare float64 `json:"chi_square"`
	PValue    float64 `json:"p_value"`
	Suspect   bool    `json:"suspect"`
}

// SRMThreshold is the p-value below which a split is flagged. It's strict on
// purpose: with large samples a real mismatch gives a vanishing p-value, and
// false alarms train people to ignore the warning.
const SRMThreshold = 0.001

// CheckSRM runs a chi-square goodness-of-fit test of observed counts against
// expected weights (any scale).
func CheckSRM(observed []float64, weights []float64) SRM {
	out := SRM{PValue: math.NaN(), ChiSquare: math.NaN()}
	if len(observed) < 2 || len(observed) != len(weights) {
		return out
	}
	var total, wsum float64
	for i := range observed {
		total += observed[i]
		wsum += weights[i]
	}
	if total == 0 || wsum == 0 {
		return out
	}
	chi := 0.0
	df := -1
	for i := range observed {
		exp := total * weights[i] / wsum
		if exp == 0 {
			continue
		}
		d := observed[i] - exp
		chi += d * d / exp
		df++
	}
	if df < 1 {
		return out
	}
	out.ChiSquare = chi
	out.PValue = ChiSquareSurvival(chi, float64(df))
	out.Suspect = out.PValue < SRMThreshold
	return out
}

// NormalCDF is the standard normal cumulative distribution function.
func NormalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

// NormalQuantile is the inverse of NormalCDF.
func NormalQuantile(p float64) float64 {
	return -math.Sqrt2 * math.Erfcinv(2*p)
}

// ChiSquareSurvival is P(X > x) for X ~ χ²(df).
func ChiSquareSurvival(x, df float64) float64 {
	if x <= 0 {
		return 1
	}
	return upperIncompleteGammaRatio(df/2, x/2)
}

// upperIncompleteGammaRatio computes Q(a, x) = Γ(a, x)/Γ(a) using the series
// for x < a+1 and a continued fraction otherwise (Numerical Recipes §6.2).
func upperIncompleteGammaRatio(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	if x < a+1 {
		sum, term := 1/a, 1/a
		for n := 1; n < 1000; n++ {
			term *= x / (a + float64(n))
			sum += term
			if math.Abs(term) < math.Abs(sum)*1e-15 {
				break
			}
		}
		return 1 - sum*math.Exp(-x+a*math.Log(x)-lg)
	}
	const tiny = 1e-300
	b := x + 1 - a
	c := 1 / tiny
	d := 1 / b
	h := d
	for i := 1; i < 1000; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-15 {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-lg) * h
}
