package formula

import (
	"math"
	"testing"
)

func TestParseAndEval(t *testing.T) {
	vars := map[string]float64{"gmv": 1000, "users": 50, "clicks": 30, "impressions": 600, "orders": 6}
	cases := []struct {
		src  string
		want float64
	}{
		{"gmv / users", 20},
		{"clicks / impressions", 0.05},
		{"orders / clicks", 0.2},
		{"(gmv - 100) / users * 2", 36},
		{"-gmv + 2 * gmv", 1000},
		{"gmv / users / 2", 10},
		{"1e3 / users", 20},
		{"gmv - users - 50", 900},
		{"+gmv", 1000},
	}
	for _, c := range cases {
		n, err := Parse(c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got := Eval(n, vars); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"", "gmv /", "(gmv", "gmv)", "gmv $ users", "sum(gmv)", "GMV", "1..2", "gmv users"} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q: expected error", src)
		}
	}
}

func TestDivisionByZero(t *testing.T) {
	n, _ := Parse("a / b")
	if v := Eval(n, map[string]float64{"a": 1, "b": 0}); !math.IsNaN(v) {
		t.Errorf("want NaN, got %v", v)
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, src := range []string{"a / (b - c)", "a - (b - c)", "(a + b) * c", "-(a + b)", "a / b / c", "a * b + c"} {
		n, _ := Parse(src)
		out := String(n)
		m, err := Parse(out)
		if err != nil {
			t.Fatalf("%s -> %s: %v", src, out, err)
		}
		vars := map[string]float64{"a": 3, "b": 7, "c": 11}
		if Eval(n, vars) != Eval(m, vars) {
			t.Errorf("%s printed as %s changes meaning", src, out)
		}
	}
}

func TestEvalGradMatchesNumeric(t *testing.T) {
	n, _ := Parse("(a * b - c) / (a + 2) + c / b")
	index := map[string]int{"a": 0, "b": 1, "c": 2}
	x := []float64{1.5, 2.5, 4}
	v, g := EvalGrad(n, x, index)
	if math.Abs(v-evalAt(n, x)) > 1e-12 {
		t.Fatalf("value mismatch")
	}
	for i := range x {
		h := 1e-6
		xp := append([]float64(nil), x...)
		xm := append([]float64(nil), x...)
		xp[i] += h
		xm[i] -= h
		num := (evalAt(n, xp) - evalAt(n, xm)) / (2 * h)
		if math.Abs(num-g[i]) > 1e-6 {
			t.Errorf("d/dx%d = %v, numeric %v", i, g[i], num)
		}
	}
}

func evalAt(n Node, x []float64) float64 {
	return Eval(n, map[string]float64{"a": x[0], "b": x[1], "c": x[2]})
}

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"gmv / users":           KindRatio,
		"clicks / impressions":  KindRatio,
		"gmv":                   KindTotal,
		"gmv + ads_gmv":         KindTotal,
		"gmv * clicks / users":  KindTotal,
		"gmv + 5":               KindMixed,
		"gmv * clicks":          KindMixed,
		"(gmv - ads_gmv) / gmv": KindRatio,
		"42":                    KindRatio,
	}
	for src, want := range cases {
		n, _ := Parse(src)
		if got := Classify(n); got != want {
			t.Errorf("%s: got %s want %s", src, got, want)
		}
	}
}

func TestSubstituteAndIdents(t *testing.T) {
	n, _ := Parse("gmv_per_user * 2 + users")
	sub, _ := Parse("gmv / users")
	out := Substitute(n, func(name string) (Node, bool) {
		if name == "gmv_per_user" {
			return sub, true
		}
		return nil, false
	})
	got := Idents(out)
	if len(got) != 2 || got[0] != "gmv" || got[1] != "users" {
		t.Errorf("idents = %v", got)
	}
	if String(out) != "gmv / users * 2 + users" {
		t.Errorf("string = %s", String(out))
	}
}
