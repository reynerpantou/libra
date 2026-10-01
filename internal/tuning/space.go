// Package tuning chooses parameter values for AB Tuning studies.
//
// A study tunes a few numeric parameters inside bounds. Each round, v0 (the
// control) keeps the current values and every treatment arm gets one
// candidate point. After a round, each arm's lift on the objective metric
// (relative to v0 in the same round) becomes an observation, and the
// algorithm picks the next round's candidates from everything observed so
// far:
//
//   - random: uniform points (pure exploration, a baseline);
//   - quasi_random: a scrambled Halton sequence, which covers the space
//     more evenly than random points;
//   - bayesian: a Gaussian process models lift over the space; candidates
//     maximise expected improvement, chosen as a batch;
//   - constrained: the same, with a Gaussian process per guardrail metric;
//     candidates maximise expected improvement times the probability that
//     every guardrail holds, inside a trust region around the best feasible
//     point that grows after an improving round and shrinks otherwise.
//
// Candidates are not mutations of one winner: they come from a model of the
// whole space, so they can move toward any promising region.
package tuning

import (
	"fmt"
	"math"
	"strings"
)

// Param is one tunable parameter.
type Param struct {
	Path    string  `json:"path"`    // dot path inside the platform's params, e.g. "search.ranking.relevance_weight"
	Type    string  `json:"type"`    // "float" or "int"
	Min     float64 `json:"min"`     // inclusive
	Max     float64 `json:"max"`     // inclusive
	Control float64 `json:"control"` // the value v0 keeps (today's production value)
	Scale   string  `json:"scale"`   // "linear" (default) or "log"
	Step    float64 `json:"step"`    // optional rounding step (ints default to 1)
}

// Space is the parameters being tuned, in order.
type Space []Param

// Validate checks bounds, types and paths.
func (s Space) Validate() error {
	if len(s) == 0 || len(s) > 10 {
		return fmt.Errorf("tune 1 to 10 parameters")
	}
	seen := map[string]bool{}
	for i := range s {
		p := &s[i]
		p.Path = strings.TrimSpace(p.Path)
		if p.Path == "" || strings.HasPrefix(p.Path, ".") || strings.HasSuffix(p.Path, ".") || strings.Contains(p.Path, "..") {
			return fmt.Errorf("parameter %d: path is required, like search.ranking.weight", i+1)
		}
		if seen[p.Path] {
			return fmt.Errorf("parameter %s appears twice", p.Path)
		}
		for q := range seen {
			if strings.HasPrefix(p.Path, q+".") || strings.HasPrefix(q, p.Path+".") {
				return fmt.Errorf("parameters %s and %s overlap", q, p.Path)
			}
		}
		seen[p.Path] = true
		if p.Type == "" {
			p.Type = "float"
		}
		if p.Type != "float" && p.Type != "int" {
			return fmt.Errorf("%s: type is float or int", p.Path)
		}
		if p.Scale == "" {
			p.Scale = "linear"
		}
		if p.Scale != "linear" && p.Scale != "log" {
			return fmt.Errorf("%s: scale is linear or log", p.Path)
		}
		if math.IsNaN(p.Min) || math.IsNaN(p.Max) || math.IsInf(p.Min, 0) || math.IsInf(p.Max, 0) || !(p.Min < p.Max) {
			return fmt.Errorf("%s: min must be below max", p.Path)
		}
		if p.Scale == "log" && p.Min <= 0 {
			return fmt.Errorf("%s: a log scale needs min > 0", p.Path)
		}
		if p.Step < 0 || p.Step > p.Max-p.Min {
			return fmt.Errorf("%s: step must be between 0 and max − min", p.Path)
		}
		if p.Type == "int" {
			if p.Step < 1 {
				p.Step = 1
			}
			p.Step = math.Round(p.Step)
			if math.Ceil(p.Min) > math.Floor(p.Max) {
				return fmt.Errorf("%s: no whole number between min and max", p.Path)
			}
		}
		if p.Control < p.Min || p.Control > p.Max {
			// v0 may sit outside the search range (e.g. tuning a new range),
			// but a broken number would break serving.
			if math.IsNaN(p.Control) || math.IsInf(p.Control, 0) {
				return fmt.Errorf("%s: control value must be a number", p.Path)
			}
		}
	}
	return nil
}

// ToUnit maps real values into [0,1]^d.
func (s Space) ToUnit(vals []float64) []float64 {
	u := make([]float64, len(s))
	for i, p := range s {
		v := vals[i]
		if p.Scale == "log" {
			u[i] = (math.Log(v) - math.Log(p.Min)) / (math.Log(p.Max) - math.Log(p.Min))
		} else {
			u[i] = (v - p.Min) / (p.Max - p.Min)
		}
		u[i] = clamp01(u[i])
	}
	return u
}

// FromUnit maps a point in [0,1]^d to real values, applying steps and
// integer rounding. The result maps back (ToUnit) to the point actually
// served.
func (s Space) FromUnit(u []float64) []float64 {
	v := make([]float64, len(s))
	for i, p := range s {
		x := clamp01(u[i])
		if p.Scale == "log" {
			v[i] = math.Exp(math.Log(p.Min) + x*(math.Log(p.Max)-math.Log(p.Min)))
		} else {
			v[i] = p.Min + x*(p.Max-p.Min)
		}
		if p.Step > 0 {
			v[i] = p.Min + math.Round((v[i]-p.Min)/p.Step)*p.Step
			if v[i] > p.Max {
				v[i] -= p.Step
			}
		} else {
			// Keep floats readable: 6 significant digits are plenty for a
			// served config.
			v[i] = roundSig(v[i], 6)
		}
		if p.Type == "int" {
			v[i] = math.Round(v[i])
		}
		v[i] = math.Max(p.Min, math.Min(p.Max, v[i]))
	}
	return v
}

// Controls is v0's values.
func (s Space) Controls() []float64 {
	out := make([]float64, len(s))
	for i, p := range s {
		out[i] = p.Control
	}
	return out
}

// Apply writes values into a copy of base at each parameter's path.
func (s Space) Apply(base map[string]any, vals []float64) map[string]any {
	out := deepCopy(base)
	for i, p := range s {
		parts := strings.Split(p.Path, ".")
		cur := out
		for _, k := range parts[:len(parts)-1] {
			next, ok := cur[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[k] = next
			}
			cur = next
		}
		if p.Type == "int" {
			cur[parts[len(parts)-1]] = int64(vals[i])
		} else {
			cur[parts[len(parts)-1]] = vals[i]
		}
	}
	return out
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopy(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) {
		return 0.5
	}
	return math.Max(0, math.Min(1, x))
}

func roundSig(x float64, n int) float64 {
	if x == 0 {
		return 0
	}
	p := math.Pow(10, float64(n)-math.Ceil(math.Log10(math.Abs(x))))
	return math.Round(x*p) / p
}
