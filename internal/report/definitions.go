// Package report builds experiment reports from the pipeline's tables and a
// business's configurable metric definitions.
package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/reynerpantou/libra/internal/formula"
	"github.com/reynerpantou/libra/internal/pipeline"
)

// UsersIdent is the built-in identifier for the number of exposed units.
const UsersIdent = "users"

// MetricDef is a configured metric: a formula over measures, `users`, and
// other metrics of the same business.
type MetricDef struct {
	ID          int64  `json:"id"`
	BusinessID  int64  `json:"business_id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Formula     string `json:"formula"`
	Format      string `json:"format"`
	Decimals    int    `json:"decimals"`
	Direction   string `json:"direction"`
}

// Definitions are all measures and metrics of one business.
type Definitions struct {
	BusinessID  int64
	Measures    map[string]pipeline.Measure
	MeasureByID map[int64]pipeline.Measure
	Metrics     map[string]MetricDef
	MetricByID  map[int64]MetricDef
	Order       []int64 // metric ids in creation order
}

// Load reads a business's definitions.
func Load(ctx context.Context, db *sql.DB, businessID int64) (*Definitions, error) {
	d := &Definitions{
		BusinessID: businessID,
		Measures:   map[string]pipeline.Measure{}, MeasureByID: map[int64]pipeline.Measure{},
		Metrics: map[string]MetricDef{}, MetricByID: map[int64]MetricDef{},
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, business_id, key, name, description, event_name, aggregation, value_field, filters
		FROM measures WHERE business_id = $1 ORDER BY id`, businessID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m pipeline.Measure
		var filters []byte
		if err := rows.Scan(&m.ID, &m.BusinessID, &m.Key, &m.Name, &m.Description, &m.EventName, &m.Aggregation, &m.ValueField, &filters); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(filters, &m.Filters)
		if m.Filters == nil {
			m.Filters = []pipeline.Filter{}
		}
		d.Measures[m.Key], d.MeasureByID[m.ID] = m, m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `
		SELECT id, business_id, key, name, description, formula, format, decimals, direction
		FROM metrics WHERE business_id = $1 ORDER BY id`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m MetricDef
		if err := rows.Scan(&m.ID, &m.BusinessID, &m.Key, &m.Name, &m.Description, &m.Formula, &m.Format, &m.Decimals, &m.Direction); err != nil {
			return nil, err
		}
		d.Metrics[m.Key], d.MetricByID[m.ID] = m, m
		d.Order = append(d.Order, m.ID)
	}
	return d, rows.Err()
}

// Expand parses src and replaces references to other metrics with their
// formulas, recursively, so the result only names measures and `users`.
// self is the key of the metric being defined ("" for an ad-hoc formula).
func (d *Definitions) Expand(src, self string) (formula.Node, error) {
	return d.expand(src, self, map[string]bool{})
}

func (d *Definitions) expand(src, self string, visiting map[string]bool) (formula.Node, error) {
	if self != "" {
		if visiting[self] {
			return nil, fmt.Errorf("metric %q refers back to itself", self)
		}
		visiting[self] = true
		defer delete(visiting, self)
	}
	n, err := formula.Parse(src)
	if err != nil {
		return nil, err
	}
	subs := map[string]formula.Node{}
	for _, name := range formula.Idents(n) {
		if name == UsersIdent {
			continue
		}
		if _, ok := d.Measures[name]; ok {
			continue
		}
		m, ok := d.Metrics[name]
		if !ok || name == self {
			if name == self {
				return nil, fmt.Errorf("metric %q refers to itself", self)
			}
			return nil, fmt.Errorf("unknown name %q — use a measure key, a metric key, or users", name)
		}
		sub, err := d.expand(m.Formula, m.Key, visiting)
		if err != nil {
			return nil, err
		}
		subs[name] = sub
	}
	return formula.Substitute(n, func(name string) (formula.Node, bool) {
		s, ok := subs[name]
		return s, ok
	}), nil
}

// MeasureKeys lists the measures an expanded formula uses.
func MeasureKeys(n formula.Node) []string {
	var out []string
	for _, name := range formula.Idents(n) {
		if name != UsersIdent {
			out = append(out, name)
		}
	}
	return out
}

// Num is a float that encodes NaN and ±Inf as JSON null.
type Num float64

func (n Num) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}

// ValidateMetric checks a metric definition against the business's other
// definitions and returns its expanded formula and kind.
func (d *Definitions) ValidateMetric(m MetricDef) (string, formula.Kind, error) {
	switch {
	case !formula.ValidIdent(m.Key):
		return "", "", fmt.Errorf("key must be lowercase letters, digits and _ (starting with a letter or _)")
	case pipeline.ReservedKeys[m.Key]:
		return "", "", fmt.Errorf("%q is reserved", m.Key)
	case strings.TrimSpace(m.Name) == "" || len(m.Name) > 120:
		return "", "", fmt.Errorf("name is required (up to 120 characters)")
	}
	if _, clash := d.Measures[m.Key]; clash {
		return "", "", fmt.Errorf("a measure is already called %q", m.Key)
	}
	switch m.Format {
	case "number", "percent", "currency":
	default:
		return "", "", fmt.Errorf("format must be number, percent or currency")
	}
	switch m.Direction {
	case "increase", "decrease", "neutral":
	default:
		return "", "", fmt.Errorf("direction must be increase, decrease or neutral")
	}
	if m.Decimals < 0 || m.Decimals > 6 {
		return "", "", fmt.Errorf("decimals must be 0–6")
	}
	// Validate as if the metric were already saved, so a change that breaks
	// another metric (a cycle, or renaming a key it uses) is caught too.
	saved := d.Metrics
	workedBefore := map[string]bool{}
	for k, v := range saved {
		if _, err := d.Expand(v.Formula, k); err == nil {
			workedBefore[k] = true
		}
	}
	d.Metrics = map[string]MetricDef{}
	for k, v := range saved {
		if v.ID != m.ID || m.ID == 0 {
			d.Metrics[k] = v
		}
	}
	d.Metrics[m.Key] = m
	defer func() { d.Metrics = saved }()
	for _, other := range d.Metrics {
		if other.Key == m.Key {
			continue
		}
		if _, err := d.Expand(other.Formula, other.Key); err != nil && workedBefore[other.Key] {
			return "", "", fmt.Errorf("this change would break metric %q: %v", other.Key, err)
		}
	}
	n, err := d.Expand(m.Formula, m.Key)
	if err != nil {
		return "", "", err
	}
	return formula.String(n), formula.Classify(n), nil
}
