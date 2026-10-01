package report

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reynerpantou/libra/internal/formula"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/stats"
)

// Options select what a report covers.
type Options struct {
	From, To  time.Time // inclusive UTC days; zero = experiment start / today
	MetricIDs []int64   // explicit metrics; wins over GroupIDs
	GroupIDs  []int64   // metric groups to show; empty = the experiment's groups plus defaults
	Alpha     float64   // significance level; 0 = 0.05
	Dimension string    // optional request attribute to break results down by
}

type Variant struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	IsControl bool   `json:"is_control"`
	Weight    int    `json:"weight"`
	Units     int64  `json:"units"`
}

type Value struct {
	VariantID int64 `json:"variant_id"`
	Units     int64 `json:"units"`
	Value     Num   `json:"value"`     // compared value (per unit for totals)
	Total     Num   `json:"total"`     // formula on totals
	StdErr    Num   `json:"std_error"` // of Value
}

type Comparison struct {
	VariantID   int64  `json:"variant_id"`
	AbsDiff     Num    `json:"abs_diff"`
	AbsCILow    Num    `json:"abs_ci_low"`
	AbsCIHigh   Num    `json:"abs_ci_high"`
	RelDiff     Num    `json:"rel_diff"`
	RelCILow    Num    `json:"rel_ci_low"`
	RelCIHigh   Num    `json:"rel_ci_high"`
	PValue      Num    `json:"p_value"`
	Significant bool   `json:"significant"`
	Testable    bool   `json:"testable"`
	Verdict     string `json:"verdict"` // better | worse | changed | flat | untestable
}

type MetricResult struct {
	MetricID    int64        `json:"metric_id"`
	Key         string       `json:"key"`
	Name        string       `json:"name"`
	Formula     string       `json:"formula"`
	Expanded    string       `json:"expanded"`
	Kind        formula.Kind `json:"kind"`
	Format      string       `json:"format"`
	Decimals    int          `json:"decimals"`
	Direction   string       `json:"direction"`
	Error       string       `json:"error,omitempty"`
	Values      []Value      `json:"values"`
	Comparisons []Comparison `json:"comparisons"`
}

type Segment struct {
	Value   string         `json:"value"` // dimension value; "" for the whole population
	Units   int64          `json:"units"`
	Metrics []MetricResult `json:"metrics"`
}

type SRM struct {
	ChiSquare Num  `json:"chi_square"`
	PValue    Num  `json:"p_value"`
	Suspect   bool `json:"suspect"`
}

type Report struct {
	ExperimentID int64     `json:"experiment_id"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	Alpha        float64   `json:"alpha"`
	Dimension    string    `json:"dimension,omitempty"`
	Variants     []Variant `json:"variants"`
	SRM          SRM       `json:"srm"`
	MultiVariant int64     `json:"excluded_multi_variant_units"`
	Segments     []Segment `json:"segments"`
	Dimensions   []string  `json:"available_dimensions"`
	DataThrough  *string   `json:"data_through,omitempty"` // last successful pipeline run
	ComputedAt   string    `json:"computed_at"`
	SegmentsCut  int       `json:"segments_not_shown,omitempty"`
	// Groups lists the metric groups shown, in order; each metric result
	// appears once in Segments and is referenced by id from its groups.
	Groups []Group `json:"groups"`
}

// Group is a set of metrics shown together in a report.
type Group struct {
	ID        int64   `json:"id"` // 0 for "all metrics" / an explicit selection
	Name      string  `json:"name"`
	Owner     string  `json:"owner"` // "Search" or "TikTok Shop (platform)"
	IsDefault bool    `json:"is_default"`
	MetricIDs []int64 `json:"metric_ids"`
}

// MaxSegments caps a dimension breakdown to its largest values.
const MaxSegments = 12

type experimentInfo struct {
	businessID  int64
	businessIDs []int64 // every business it runs in (defaults of each apply)
	groupIDs    []int64
	startedAt   sql.NullTime
	endedAt     sql.NullTime
	createdAt   time.Time
	variants    []Variant
}

var ErrNotFound = errors.New("experiment not found")

func loadExperiment(ctx context.Context, db *sql.DB, id int64) (*experimentInfo, error) {
	e := &experimentInfo{}
	var groups string
	var bids string
	err := db.QueryRowContext(ctx, `SELECT business_id, array_to_string(business_ids, ','), array_to_string(metric_group_ids, ','), started_at, ended_at, created_at FROM experiments WHERE id = $1`, id).
		Scan(&e.businessID, &bids, &groups, &e.startedAt, &e.endedAt, &e.createdAt)
	e.groupIDs = parseIDs(groups)
	e.businessIDs = parseIDs(bids)
	if len(e.businessIDs) == 0 {
		e.businessIDs = []int64{e.businessID}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, key, name, is_control, weight FROM variants WHERE experiment_id = $1 ORDER BY position, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v Variant
		if err := rows.Scan(&v.ID, &v.Key, &v.Name, &v.IsControl, &v.Weight); err != nil {
			return nil, err
		}
		e.variants = append(e.variants, v)
	}
	return e, rows.Err()
}

func day(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// window fills default dates: from the experiment's start to its end or today.
func (e *experimentInfo) window(o Options) (time.Time, time.Time) {
	from, to := o.From, o.To
	if from.IsZero() {
		from = e.createdAt
		if e.startedAt.Valid {
			from = e.startedAt.Time
		}
	}
	if to.IsZero() {
		to = time.Now()
		if e.endedAt.Valid {
			to = e.endedAt.Time
		}
	}
	return day(from), day(to)
}

func parseIDs(raw string) []int64 {
	out := []int64{} // never null in JSON
	for _, p := range strings.Split(raw, ",") {
		var id int64
		if _, err := fmt.Sscan(strings.TrimSpace(p), &id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// catalog loads each scope's definitions once per report.
type catalog struct {
	ctx      context.Context
	db       *sql.DB
	byScope  map[Scope]*Definitions
	measures map[int64]pipeline.Measure
}

func newCatalog(ctx context.Context, db *sql.DB) *catalog {
	return &catalog{ctx: ctx, db: db, byScope: map[Scope]*Definitions{}, measures: map[int64]pipeline.Measure{}}
}

func (c *catalog) defs(sc Scope) (*Definitions, error) {
	if d, ok := c.byScope[sc]; ok {
		return d, nil
	}
	d, err := LoadScope(c.ctx, c.db, sc)
	if err != nil {
		return nil, err
	}
	c.byScope[sc] = d
	return d, nil
}

// DefaultGroupIDs are the groups every experiment of a business shows: the
// default groups of its platform and of the business itself.
func DefaultGroupIDs(ctx context.Context, db *sql.DB, businessIDs []int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT g.id FROM metric_groups g
		WHERE g.is_default AND (g.business_id = ANY($1::bigint[]) OR g.platform_id IN (SELECT platform_id FROM businesses WHERE id = ANY($1::bigint[])))
		ORDER BY g.platform_id NULLS LAST, array_position($1::bigint[], g.business_id), g.id`, businessIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func loadGroups(ctx context.Context, db *sql.DB, ids []int64) ([]Group, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT g.id, g.name, g.is_default, array_to_string(g.metric_ids, ','),
		       COALESCE(b.name, p.name || ' (platform)')
		FROM metric_groups g LEFT JOIN businesses b ON b.id = g.business_id LEFT JOIN platforms p ON p.id = g.platform_id
		WHERE g.id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[int64]Group{}
	for rows.Next() {
		var g Group
		var raw string
		if err := rows.Scan(&g.ID, &g.Name, &g.IsDefault, &raw, &g.Owner); err != nil {
			return nil, err
		}
		g.MetricIDs = parseIDs(raw)
		by[g.ID] = g
	}
	var out []Group
	for _, id := range ids {
		if g, ok := by[id]; ok {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

func loadMetricDefs(ctx context.Context, db *sql.DB, ids []int64) (map[int64]MetricDef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(business_id, 0), COALESCE(platform_id, 0), key, name, description, formula, format, decimals, direction
		FROM metrics WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MetricDef{}
	for rows.Next() {
		var m MetricDef
		if err := rows.Scan(&m.ID, &m.BusinessID, &m.PlatformID, &m.Key, &m.Name, &m.Description, &m.Formula, &m.Format, &m.Decimals, &m.Direction); err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

// selectMetrics resolves which groups and metrics a report shows.
func selectMetrics(ctx context.Context, db *sql.DB, e *experimentInfo, o Options) ([]Group, []MetricDef, error) {
	var groups []Group
	switch {
	case len(o.MetricIDs) > 0:
		groups = []Group{{Name: "Selected metrics", MetricIDs: o.MetricIDs}}
	default:
		ids := o.GroupIDs
		if len(ids) == 0 {
			defaults, err := DefaultGroupIDs(ctx, db, e.businessIDs)
			if err != nil {
				return nil, nil, err
			}
			seen := map[int64]bool{}
			for _, id := range append(defaults, e.groupIDs...) {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		loaded, err := loadGroups(ctx, db, ids)
		if err != nil {
			return nil, nil, err
		}
		// An empty group (e.g. a platform's new default group) has
		// nothing to show.
		for _, g := range loaded {
			if len(g.MetricIDs) > 0 {
				groups = append(groups, g)
			}
		}
	}
	if len(groups) == 0 {
		defs, err := Load(ctx, db, e.businessID)
		if err != nil {
			return nil, nil, err
		}
		groups = []Group{{Name: "All metrics", MetricIDs: defs.Order}}
	}
	var all []int64
	seen := map[int64]bool{}
	for _, g := range groups {
		for _, id := range g.MetricIDs {
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
	}
	defs, err := loadMetricDefs(ctx, db, all)
	if err != nil {
		return nil, nil, err
	}
	var out []MetricDef
	for _, id := range all {
		if m, ok := defs[id]; ok {
			out = append(out, m)
		}
	}
	return groups, out, nil
}

type compiled struct {
	def      MetricDef
	node     formula.Node
	expanded string
	kind     formula.Kind
	err      error
}

// cell is the moments of one variant in one segment.
type cell struct {
	variant int64
	segment string
	m       stats.Moments
}

// Compute builds the report for an experiment.
func Compute(ctx context.Context, db *sql.DB, expID int64, o Options) (*Report, error) {
	e, err := loadExperiment(ctx, db, expID)
	if err != nil {
		return nil, err
	}
	if o.Alpha <= 0 || o.Alpha >= 0.5 {
		o.Alpha = 0.05
	}
	from, to := e.window(o)
	groups, metrics, err := selectMetrics(ctx, db, e, o)
	if err != nil {
		return nil, err
	}
	cat := newCatalog(ctx, db)
	comps, measureIDs, index := compileAll(cat, metrics)

	cells, err := moments(ctx, db, expID, from, to, measureIDs, cat.measures, o.Dimension)
	if err != nil {
		return nil, err
	}

	r := &Report{
		ExperimentID: expID, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"),
		Alpha: o.Alpha, Dimension: o.Dimension, Variants: e.variants, Segments: []Segment{},
		ComputedAt: time.Now().UTC().Format(time.RFC3339), Groups: groups,
	}

	// Whole-population totals per variant (and SRM) come from summing segments.
	overall := map[int64]stats.Moments{}
	segUnits := map[string]int64{}
	for _, c := range cells {
		overall[c.variant] = addMoments(overall[c.variant], c.m)
		segUnits[c.segment] += int64(c.m.N)
	}
	var observed, weights []float64
	for i := range r.Variants {
		r.Variants[i].Units = int64(overall[r.Variants[i].ID].N)
		observed = append(observed, overall[r.Variants[i].ID].N)
		weights = append(weights, float64(r.Variants[i].Weight))
	}
	srm := stats.CheckSRM(observed, weights)
	r.SRM = SRM{Num(srm.ChiSquare), Num(srm.PValue), srm.Suspect}

	if o.Dimension == "" {
		r.Segments = append(r.Segments, buildSegment("", overall, e.variants, comps, index, o.Alpha))
	} else {
		bySeg := map[string]map[int64]stats.Moments{}
		for _, c := range cells {
			if bySeg[c.segment] == nil {
				bySeg[c.segment] = map[int64]stats.Moments{}
			}
			bySeg[c.segment][c.variant] = c.m
		}
		names := make([]string, 0, len(bySeg))
		for s := range bySeg {
			names = append(names, s)
		}
		sort.Slice(names, func(i, j int) bool {
			if segUnits[names[i]] != segUnits[names[j]] {
				return segUnits[names[i]] > segUnits[names[j]]
			}
			return names[i] < names[j]
		})
		if len(names) > MaxSegments {
			r.SegmentsCut = len(names) - MaxSegments
			names = names[:MaxSegments]
		}
		for _, s := range names {
			r.Segments = append(r.Segments, buildSegment(s, bySeg[s], e.variants, comps, index, o.Alpha))
		}
	}

	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM assignments WHERE experiment_id = $1 AND multi_variant AND first_day BETWEEN $2 AND $3`,
		expID, from, to).Scan(&r.MultiVariant)
	r.Dimensions = dimensions(ctx, db, expID)
	var through sql.NullTime
	_ = db.QueryRowContext(ctx, `SELECT max(finished_at) FROM pipeline_runs WHERE status = 'succeeded'`).Scan(&through)
	if through.Valid {
		s := through.Time.UTC().Format(time.RFC3339)
		r.DataThrough = &s
	}
	return r, nil
}

// compileAll expands every metric in its own scope (so a group can mix
// businesses) and gives each measure used a column. Measures are renamed
// m_<id> internally, because two businesses may use the same key. `users`
// takes the last column.
func compileAll(cat *catalog, metrics []MetricDef) ([]compiled, []int64, map[string]int) {
	var comps []compiled
	index := map[string]int{}
	var measureIDs []int64
	for _, m := range metrics {
		c := compiled{def: m}
		defs, err := cat.defs(m.ScopeOf())
		if err != nil {
			c.err = err
			comps = append(comps, c)
			continue
		}
		var node formula.Node
		node, c.err = defs.Expand(m.Formula, m.Key)
		if c.err == nil {
			c.expanded = formula.String(node)
			c.kind = formula.Classify(node)
			c.node = formula.Substitute(node, func(name string) (formula.Node, bool) {
				ms, ok := defs.Measures[name]
				if !ok {
					return nil, false
				}
				ident := fmt.Sprintf("m_%d", ms.ID)
				if _, seen := index[ident]; !seen {
					index[ident] = len(measureIDs)
					measureIDs = append(measureIDs, ms.ID)
					cat.measures[ms.ID] = ms
				}
				return formula.Ident{Name: ident}, true
			})
		}
		comps = append(comps, c)
	}
	index[UsersIdent] = len(measureIDs)
	return comps, measureIDs, index
}

func addMoments(a, b stats.Moments) stats.Moments {
	if a.Sum == nil {
		k := len(b.Sum)
		a = stats.Moments{Sum: make([]float64, k), Cross: make([][]float64, k)}
		for i := range a.Cross {
			a.Cross[i] = make([]float64, k)
		}
	}
	a.N += b.N
	for i := range b.Sum {
		a.Sum[i] += b.Sum[i]
		for j := range b.Sum {
			a.Cross[i][j] += b.Cross[i][j]
		}
	}
	return a
}

func buildSegment(name string, byVariant map[int64]stats.Moments, variants []Variant, comps []compiled, index map[string]int, alpha float64) Segment {
	seg := Segment{Value: name, Metrics: []MetricResult{}}
	for _, v := range variants {
		seg.Units += int64(byVariant[v.ID].N)
	}
	var control *Variant
	for i := range variants {
		if variants[i].IsControl {
			control = &variants[i]
			break
		}
	}
	for _, c := range comps {
		mr := MetricResult{
			MetricID: c.def.ID, Key: c.def.Key, Name: c.def.Name, Formula: c.def.Formula,
			Expanded: c.expanded, Kind: c.kind, Format: c.def.Format, Decimals: c.def.Decimals,
			Direction: c.def.Direction, Values: []Value{}, Comparisons: []Comparison{},
		}
		if c.err != nil {
			mr.Error = c.err.Error()
			seg.Metrics = append(seg.Metrics, mr)
			continue
		}
		est := map[int64]stats.Estimate{}
		for _, v := range variants {
			m := withUsers(byVariant[v.ID], len(index))
			est[v.ID] = stats.EstimateOf(c.node, index, m)
			x := est[v.ID]
			mr.Values = append(mr.Values, Value{VariantID: v.ID, Units: int64(x.N), Value: Num(x.Value), Total: Num(x.Total), StdErr: Num(x.StdErr)})
		}
		if control != nil {
			for _, v := range variants {
				if v.ID == control.ID {
					continue
				}
				cmp := stats.Compare(est[v.ID], est[control.ID], alpha)
				mr.Comparisons = append(mr.Comparisons, Comparison{
					VariantID: v.ID, AbsDiff: Num(cmp.AbsDiff), AbsCILow: Num(cmp.AbsCILow), AbsCIHigh: Num(cmp.AbsCIHigh),
					RelDiff: Num(cmp.RelDiff), RelCILow: Num(cmp.RelCILow), RelCIHigh: Num(cmp.RelCIHigh),
					PValue: Num(cmp.PValue), Significant: cmp.Significant, Testable: cmp.Testable,
					Verdict: verdict(cmp, c.def.Direction),
				})
			}
		}
		seg.Metrics = append(seg.Metrics, mr)
	}
	return seg
}

func verdict(c stats.Comparison, direction string) string {
	switch {
	case !c.Testable:
		return "untestable"
	case !c.Significant:
		return "flat"
	case direction == "neutral":
		return "changed"
	case (c.AbsDiff > 0) == (direction == "increase"):
		return "better"
	default:
		return "worse"
	}
}

// withUsers adds the `users` column: every unit contributes 1.
func withUsers(m stats.Moments, k int) stats.Moments {
	out := stats.Moments{N: m.N, Sum: make([]float64, k), Cross: make([][]float64, k)}
	for i := range out.Cross {
		out.Cross[i] = make([]float64, k)
	}
	u := k - 1
	for i := 0; i < u && i < len(m.Sum); i++ {
		out.Sum[i] = m.Sum[i]
		for j := 0; j < u && j < len(m.Sum); j++ {
			out.Cross[i][j] = m.Cross[i][j]
		}
		out.Cross[i][u], out.Cross[u][i] = m.Sum[i], m.Sum[i]
	}
	out.Sum[u], out.Cross[u][u] = m.N, m.N
	return out
}

// moments computes, per variant (and dimension value), the unit count and
// the sums and cross sums of every measure over each unit's post-exposure
// window — one pass in SQL, no per-unit data leaves the database.
func moments(ctx context.Context, db *sql.DB, expID int64, from, to time.Time, measureIDs []int64, measures map[int64]pipeline.Measure, dimension string) ([]cell, error) {
	args := []any{expID, from, to}
	seg := "''"
	if dimension != "" {
		args = append(args, dimension)
		seg = fmt.Sprintf("COALESCE(NULLIF(dims->>($%d::text), ''), '(not set)')", len(args))
	}
	units := fmt.Sprintf(`SELECT unit_id, unit_type, variant_id, first_day, %s AS seg FROM assignments
		WHERE experiment_id = $1 AND NOT multi_variant AND first_day BETWEEN $2 AND $3`, seg)
	return momentsOf(ctx, db, units, args, 3, measureIDs, measures)
}

// momentsOf runs the moments query over a set of units: units is a query
// returning (unit_id, unit_type, variant_id, first_day, seg); each unit's
// measures count from its first_day through the day in argument endArg.
func momentsOf(ctx context.Context, db *sql.DB, units string, args []any, endArg int, measureIDs []int64, measures map[int64]pipeline.Measure) ([]cell, error) {
	k := len(measureIDs)
	var q strings.Builder
	fmt.Fprintf(&q, `WITH u AS (%s)`, units)
	if k > 0 {
		args = append(args, measureIDs)
		q.WriteString(`, m AS (SELECT u.unit_id, u.variant_id, u.seg`)
		for i, id := range measureIDs {
			agg := pipeline.WindowAgg(measures[id].Aggregation)
			fmt.Fprintf(&q, `, COALESCE(%s(d.value) FILTER (WHERE d.measure_id = %d), 0) AS m%d`, agg, id, i)
		}
		fmt.Fprintf(&q, ` FROM u LEFT JOIN unit_measure_daily d
			ON d.unit_id = u.unit_id AND d.unit_type = u.unit_type AND d.measure_id = ANY($%d::bigint[]) AND d.day BETWEEN u.first_day AND $%d::date
			GROUP BY u.unit_id, u.variant_id, u.seg)`, len(args), endArg)
	} else {
		q.WriteString(`, m AS (SELECT * FROM u)`)
	}
	q.WriteString(` SELECT variant_id, seg, count(*)`)
	for i := 0; i < k; i++ {
		fmt.Fprintf(&q, `, sum(m%d)`, i)
	}
	for i := 0; i < k; i++ {
		for j := i; j < k; j++ {
			fmt.Fprintf(&q, `, sum(m%d * m%d)`, i, j)
		}
	}
	q.WriteString(` FROM m GROUP BY variant_id, seg`)

	rows, err := db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cell
	for rows.Next() {
		c := cell{m: stats.Moments{Sum: make([]float64, k), Cross: make([][]float64, k)}}
		for i := range c.m.Cross {
			c.m.Cross[i] = make([]float64, k)
		}
		var n int64
		dest := []any{&c.variant, &c.segment, &n}
		sums := make([]sql.NullFloat64, k+k*(k+1)/2)
		for i := range sums {
			dest = append(dest, &sums[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		c.m.N = float64(n)
		p := 0
		for i := 0; i < k; i++ {
			c.m.Sum[i] = sums[p].Float64
			p++
		}
		for i := 0; i < k; i++ {
			for j := i; j < k; j++ {
				c.m.Cross[i][j], c.m.Cross[j][i] = sums[p].Float64, sums[p].Float64
				p++
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// dimensions lists the attribute names seen on this experiment's units.
func dimensions(ctx context.Context, db *sql.DB, expID int64) []string {
	out := []string{}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT jsonb_object_keys(dims) FROM (
			SELECT dims FROM assignments WHERE experiment_id = $1 LIMIT 2000
		) s ORDER BY 1 LIMIT 50`, expID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// TrendPoint is one day of a cumulative metric trend.
type TrendPoint struct {
	Day    string  `json:"day"`
	Values []Value `json:"values"`
	// Relative lift of each treatment over control, with its interval.
	Comparisons []Comparison `json:"comparisons"`
}

// MaxTrendDays bounds the per-day recomputation.
const MaxTrendDays = 60

// Trend recomputes one metric cumulatively for each day of the window, so a
// chart shows how the estimate and its interval settle over time.
func Trend(ctx context.Context, db *sql.DB, expID, metricID int64, o Options) ([]TrendPoint, error) {
	e, err := loadExperiment(ctx, db, expID)
	if err != nil {
		return nil, err
	}
	if o.Alpha <= 0 || o.Alpha >= 0.5 {
		o.Alpha = 0.05
	}
	from, to := e.window(o)
	if days := int(to.Sub(from).Hours()/24) + 1; days > MaxTrendDays {
		from = to.AddDate(0, 0, -(MaxTrendDays - 1))
	}
	found, err := loadMetricDefs(ctx, db, []int64{metricID})
	if err != nil {
		return nil, err
	}
	m, ok := found[metricID]
	if !ok {
		return nil, fmt.Errorf("metric %d doesn't exist", metricID)
	}
	cat := newCatalog(ctx, db)
	comps, measureIDs, index := compileAll(cat, []MetricDef{m})
	if comps[0].err != nil {
		return nil, comps[0].err
	}
	var days []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	// Each day is an independent query; run a few at once.
	out := make([]TrendPoint, len(days))
	errs := make([]error, len(days))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, d := range days {
		wg.Add(1)
		go func(i int, d time.Time) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cells, err := moments(ctx, db, expID, from, d, measureIDs, cat.measures, "")
			if err != nil {
				errs[i] = err
				return
			}
			byVar := map[int64]stats.Moments{}
			for _, c := range cells {
				byVar[c.variant] = c.m
			}
			seg := buildSegment("", byVar, e.variants, comps, index, o.Alpha)
			out[i] = TrendPoint{Day: d.Format("2006-01-02"), Values: seg.Metrics[0].Values, Comparisons: seg.Metrics[0].Comparisons}
		}(i, d)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// Preview evaluates an ad-hoc formula for a business over all units that
// had any event in the window — a quick sanity check while writing a formula,
// before any experiment uses it.
func Preview(ctx context.Context, db *sql.DB, defs *Definitions, src string, from, to time.Time) (map[string]any, error) {
	node, err := defs.Expand(src, "")
	if err != nil {
		return nil, err
	}
	keys := MeasureKeys(node)
	vars := map[string]float64{}
	var users int64
	if err := db.QueryRowContext(ctx, `
		SELECT count(DISTINCT unit_id) FROM unit_measure_daily
		WHERE measure_id = ANY($1::bigint[]) AND unit_type = 'user_id' AND day BETWEEN $2 AND $3`, measureIDsOf(defs), from, to).Scan(&users); err != nil {
		return nil, err
	}
	vars[UsersIdent] = float64(users)
	totals := map[string]float64{}
	for _, k := range keys {
		m := defs.Measures[k]
		var v sql.NullFloat64
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT sum(v) FROM (SELECT %s(value) AS v FROM unit_measure_daily WHERE measure_id = $1 AND unit_type = 'user_id' AND day BETWEEN $2 AND $3 GROUP BY unit_id) s`,
			pipeline.WindowAgg(m.Aggregation)), m.ID, from, to).Scan(&v); err != nil {
			return nil, err
		}
		vars[k], totals[k] = v.Float64, v.Float64
	}
	return map[string]any{
		"value":    Num(formula.Eval(node, vars)),
		"expanded": formula.String(node),
		"kind":     formula.Classify(node),
		"users":    users,
		"measures": totals,
		"from":     from.Format("2006-01-02"),
		"to":       to.Format("2006-01-02"),
	}, nil
}

func measureIDsOf(d *Definitions) []int64 {
	out := []int64{}
	for id := range d.MeasureByID {
		out = append(out, id)
	}
	return out
}

// ArmResults measures a set of variants over a time window, the way a
// tuning round is analysed: units are those exposed to exactly one of the
// variants during [from, to); each unit's measures count from the day of its
// first exposure in the window through the window's last day. Comparisons
// are against the control variant.
func ArmResults(ctx context.Context, db *sql.DB, expID int64, variants []Variant, from, to time.Time, metricIDs []int64, alpha float64) (Segment, error) {
	if alpha <= 0 || alpha >= 0.5 {
		alpha = 0.05
	}
	defs, err := loadMetricDefs(ctx, db, metricIDs)
	if err != nil {
		return Segment{}, err
	}
	var metrics []MetricDef
	for _, id := range metricIDs {
		if d, ok := defs[id]; ok {
			metrics = append(metrics, d)
		}
	}
	cat := newCatalog(ctx, db)
	comps, measureIDs, index := compileAll(cat, metrics)
	ids := make([]int64, len(variants))
	for i, v := range variants {
		ids[i] = v.ID
	}
	lastDay := day(to.Add(-time.Nanosecond))
	args := []any{expID, ids, from, to, lastDay}
	units := `SELECT unit_id, unit_type, min(variant_id) AS variant_id, (min(ts) AT TIME ZONE 'UTC')::date AS first_day, '' AS seg
		FROM exposures
		WHERE experiment_id = $1 AND variant_id = ANY($2::bigint[]) AND ts >= $3 AND ts < $4
		GROUP BY unit_id, unit_type
		HAVING count(DISTINCT variant_id) = 1`
	cells, err := momentsOf(ctx, db, units, args, 5, measureIDs, cat.measures)
	if err != nil {
		return Segment{}, err
	}
	byVariant := map[int64]stats.Moments{}
	for _, c := range cells {
		byVariant[c.variant] = addMoments(byVariant[c.variant], c.m)
	}
	for i := range variants {
		variants[i].Units = int64(byVariant[variants[i].ID].N)
	}
	return buildSegment("", byVariant, variants, comps, index, alpha), nil
}
