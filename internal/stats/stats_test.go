package stats

import (
	"math"
	"math/rand"
	"testing"

	"github.com/reynerpantou/libra/internal/formula"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestDistributions(t *testing.T) {
	if !approx(NormalCDF(1.959963984540054), 0.975, 1e-9) {
		t.Error("NormalCDF")
	}
	if !approx(NormalQuantile(0.975), 1.959963984540054, 1e-9) {
		t.Error("NormalQuantile")
	}
	// Reference values from scipy.stats.chi2.sf.
	cases := []struct{ x, df, want float64 }{
		{3.841458820694124, 1, 0.05},
		{10.827566170662733, 1, 0.001},
		{5.991464547107979, 2, 0.05},
		{1, 3, 0.8012519569012008},
		{30, 10, 0.0008566412},
	}
	for _, c := range cases {
		if got := ChiSquareSurvival(c.x, c.df); !approx(got, c.want, 1e-7) {
			t.Errorf("chi2.sf(%v, %v) = %v, want %v", c.x, c.df, got, c.want)
		}
	}
}

func TestSRM(t *testing.T) {
	ok := CheckSRM([]float64{5010, 4990}, []float64{50, 50})
	if ok.Suspect {
		t.Errorf("balanced split flagged: %+v", ok)
	}
	bad := CheckSRM([]float64{5300, 4700}, []float64{50, 50})
	if !bad.Suspect {
		t.Errorf("skewed split not flagged: %+v", bad)
	}
	uneven := CheckSRM([]float64{2000, 8000}, []float64{20, 80})
	if uneven.Suspect {
		t.Errorf("correct 20/80 split flagged: %+v", uneven)
	}
}

// momentsOf builds Moments from per-unit rows.
func momentsOf(rows [][]float64) Moments {
	k := len(rows[0])
	m := Moments{Sum: make([]float64, k), Cross: make([][]float64, k)}
	for i := range m.Cross {
		m.Cross[i] = make([]float64, k)
	}
	for _, r := range rows {
		m.N++
		for i := 0; i < k; i++ {
			m.Sum[i] += r[i]
			for j := 0; j < k; j++ {
				m.Cross[i][j] += r[i] * r[j]
			}
		}
	}
	return m
}

func TestEstimateMeanMatchesTTestVariance(t *testing.T) {
	// For f = x / users (a plain mean), the delta method equals s²/n.
	rows := [][]float64{{1, 1}, {3, 1}, {5, 1}, {7, 1}}
	node, _ := formula.Parse("x / users")
	e := EstimateOf(node, map[string]int{"x": 0, "users": 1}, momentsOf(rows))
	if !approx(e.Value, 4, 1e-12) || !approx(e.Total, 16/4.0, 1e-12) {
		t.Fatalf("value %v total %v", e.Value, e.Total)
	}
	// sample variance of 1,3,5,7 = 20/3; /n=4
	if !approx(e.Variance, 20.0/3/4, 1e-12) {
		t.Errorf("variance %v", e.Variance)
	}
}

func TestRatioMetricCoverage(t *testing.T) {
	// Simulate CTR = clicks / impressions under the null (identical
	// variants) and check the 95% interval covers the truth ~95% of the time.
	rng := rand.New(rand.NewSource(7))
	node, _ := formula.Parse("clicks / impressions")
	index := map[string]int{"clicks": 0, "impressions": 1}
	gen := func() Moments {
		rows := make([][]float64, 2000)
		for i := range rows {
			imp := float64(1 + rng.Intn(20))
			clicks := 0.0
			p := 0.05 + 0.1*rng.Float64() // per-user heterogeneity
			for j := 0; j < int(imp); j++ {
				if rng.Float64() < p {
					clicks++
				}
			}
			rows[i] = []float64{clicks, imp}
		}
		return momentsOf(rows)
	}
	falsePositives := 0
	const runs = 400
	for r := 0; r < runs; r++ {
		a := EstimateOf(node, index, gen())
		b := EstimateOf(node, index, gen())
		if Compare(a, b, 0.05).Significant {
			falsePositives++
		}
	}
	rate := float64(falsePositives) / runs
	if rate < 0.02 || rate > 0.09 {
		t.Errorf("false positive rate %.3f, want about 0.05", rate)
	}
}

func TestCompareDetectsLift(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	node, _ := formula.Parse("gmv / users")
	index := map[string]int{"gmv": 0, "users": 1}
	gen := func(mu float64) Moments {
		rows := make([][]float64, 5000)
		for i := range rows {
			rows[i] = []float64{math.Max(0, rng.NormFloat64()*10+mu), 1}
		}
		return momentsOf(rows)
	}
	c := Compare(EstimateOf(node, index, gen(22)), EstimateOf(node, index, gen(20)), 0.05)
	if !c.Significant || c.RelDiff < 0.05 || c.RelCILow > c.RelDiff || c.RelCIHigh < c.RelDiff {
		t.Errorf("comparison %+v", c)
	}
}

func TestCompareSmallSampleUntestable(t *testing.T) {
	node, _ := formula.Parse("x / users")
	index := map[string]int{"x": 0, "users": 1}
	m := momentsOf([][]float64{{1, 1}, {2, 1}, {3, 1}})
	c := Compare(EstimateOf(node, index, m), EstimateOf(node, index, m), 0.05)
	if c.Testable || !math.IsNaN(c.PValue) {
		t.Errorf("expected untestable, got %+v", c)
	}
}
