package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/reynerpantou/libra/internal/formula"
)

// Filter narrows the events a measure counts, e.g. source = search.
type Filter struct {
	Field  string   `json:"field"` // an event property, or "value"
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// FilterOps are the supported filter operators.
var FilterOps = []string{"eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "exists", "not_exists"}

// Aggregations: how one unit's events on one day become a number.
//
//	count  number of matching events
//	sum    sum of the value (or of a numeric property)
//	max    largest value
//	any    1 if the unit had at least one matching event (e.g. buyers)
var Aggregations = []string{"count", "sum", "max", "any"}

// Measure turns raw events into one number per unit per day.
type Measure struct {
	ID          int64    `json:"id"`
	BusinessID  int64    `json:"business_id,omitempty"` // set for business measures
	PlatformID  int64    `json:"platform_id,omitempty"` // set for platform measures (all its businesses' events)
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	EventName   string   `json:"event_name"`
	Aggregation string   `json:"aggregation"`
	ValueField  string   `json:"value_field"`
	Filters     []Filter `json:"filters"`
}

var propName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ReservedKeys can't be used for measures or metrics.
var ReservedKeys = map[string]bool{"users": true}

// Validate checks a measure definition.
func (m Measure) Validate() error {
	switch {
	case !formula.ValidIdent(m.Key):
		return errors.New("key must be lowercase letters, digits and _ (starting with a letter or _)")
	case ReservedKeys[m.Key]:
		return fmt.Errorf("%q is reserved", m.Key)
	case strings.TrimSpace(m.Name) == "" || len(m.Name) > 120:
		return errors.New("name is required (up to 120 characters)")
	case strings.TrimSpace(m.EventName) == "" || len(m.EventName) > 120:
		return errors.New("event name is required")
	case !contains(Aggregations, m.Aggregation):
		return fmt.Errorf("aggregation must be one of %s", strings.Join(Aggregations, ", "))
	case m.ValueField != "" && !propName.MatchString(m.ValueField):
		return errors.New("value field must be a property name")
	case len(m.Filters) > 20:
		return errors.New("at most 20 filters")
	}
	for _, f := range m.Filters {
		if f.Field != "value" && !propName.MatchString(f.Field) {
			return fmt.Errorf("filter field %q isn't a valid property name", f.Field)
		}
		if !contains(FilterOps, f.Op) {
			return fmt.Errorf("unknown filter operator %q", f.Op)
		}
		if f.Op != "exists" && f.Op != "not_exists" && len(f.Values) == 0 {
			return fmt.Errorf("filter on %q needs a value", f.Field)
		}
		if len(f.Values) > 100 {
			return errors.New("a filter can list at most 100 values")
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// WindowAgg is how daily values combine over a report window.
func WindowAgg(aggregation string) string {
	if aggregation == "max" || aggregation == "any" {
		return "MAX"
	}
	return "SUM"
}

// sqlArgs accumulates positional parameters.
type sqlArgs struct{ list []any }

func (a *sqlArgs) add(v any) string {
	a.list = append(a.list, v)
	return fmt.Sprintf("$%d", len(a.list))
}

// compile returns the per-row aggregate expression and the WHERE conditions
// (beyond business, event name and time) for a measure. Every user-supplied
// string travels as a parameter; nothing is spliced into SQL.
func (m Measure) compile(args *sqlArgs) (agg string, conds []string) {
	val := "value"
	if m.ValueField != "" {
		val = "libra_num(props->>" + args.add(m.ValueField) + "::text)"
	}
	switch m.Aggregation {
	case "count":
		agg = "COUNT(*)::float8"
	case "sum":
		agg = "COALESCE(SUM(" + val + "), 0)"
	case "max":
		agg = "MAX(" + val + ")"
		conds = append(conds, val+" IS NOT NULL")
	case "any":
		agg = "1::float8"
	}
	for _, f := range m.Filters {
		conds = append(conds, compileFilter(f, args))
	}
	return agg, conds
}

func compileFilter(f Filter, args *sqlArgs) string {
	first := ""
	if len(f.Values) > 0 {
		first = f.Values[0]
	}
	if f.Field == "value" {
		switch f.Op {
		case "exists":
			return "true"
		case "not_exists":
			return "false"
		case "in", "not_in":
			cond := "value::text = ANY(" + args.add(f.Values) + "::text[])"
			if f.Op == "not_in" {
				return "NOT (" + cond + ")"
			}
			return cond
		}
		return "value " + sqlOp(f.Op) + " libra_num(" + args.add(first) + ")"
	}
	key := args.add(f.Field) + "::text"
	text := "(props->>" + key + ")"
	switch f.Op {
	case "exists":
		return "jsonb_exists(props, " + key + ")"
	case "not_exists":
		return "NOT jsonb_exists(props, " + key + ")"
	case "eq":
		return text + " = " + args.add(first)
	case "neq":
		return text + " IS DISTINCT FROM " + args.add(first)
	case "in":
		return text + " = ANY(" + args.add(f.Values) + "::text[])"
	case "not_in":
		return "(" + text + " IS NULL OR NOT (" + text + " = ANY(" + args.add(f.Values) + "::text[])))"
	default: // numeric comparisons
		return "libra_num" + text + " " + sqlOp(f.Op) + " libra_num(" + args.add(first) + ")"
	}
}

func sqlOp(op string) string {
	switch op {
	case "eq":
		return "="
	case "neq":
		return "<>"
	case "gt":
		return ">"
	case "gte":
		return ">="
	case "lt":
		return "<"
	case "lte":
		return "<="
	}
	return "="
}

// DescribeFilter renders a filter for people ("source = search").
func DescribeFilter(f Filter) string {
	switch f.Op {
	case "exists":
		return f.Field + " is set"
	case "not_exists":
		return f.Field + " is not set"
	case "in":
		return f.Field + " in (" + strings.Join(f.Values, ", ") + ")"
	case "not_in":
		return f.Field + " not in (" + strings.Join(f.Values, ", ") + ")"
	}
	v := ""
	if len(f.Values) > 0 {
		v = f.Values[0]
	}
	return f.Field + " " + sqlOp(f.Op) + " " + v
}
