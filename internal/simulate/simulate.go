// Package simulate seeds a demo setup and generates realistic synthetic
// traffic, so every screen has data before real services are integrated.
//
// The demo business is "search": users search, see results (impressions),
// click, and order; some orders come from ads. Units are assigned through
// the real assignment engine, and treatments have built-in effects, so the
// reports show genuine, recoverable differences.
package simulate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/serving"
)

// ErrSeeded means the demo business already exists.
var ErrSeeded = errors.New(`the demo business "search" already exists`)

type measureSpec struct {
	key, name, desc, event, agg, valueField string
	filters                                 []map[string]any
}

type metricSpec struct {
	key, name, desc, formula, format, direction string
	decimals                                    int
}

var demoMeasures = []measureSpec{
	{"searches", "Searches", "Search queries issued", "search", "count", "", nil},
	{"search_impressions", "Search impressions", "Results shown (the impressions property of each search)", "search", "sum", "impressions", nil},
	{"search_clicks", "Search clicks", "Clicks on search results", "search_click", "count", "", nil},
	{"search_orders", "Search orders", "Orders attributed to search", "order", "count", "", []map[string]any{{"field": "source", "op": "eq", "values": []string{"search"}}}},
	{"search_gmv", "Search GMV", "Order value attributed to search", "order", "sum", "", []map[string]any{{"field": "source", "op": "eq", "values": []string{"search"}}}},
	{"search_ads_gmv", "Search ads GMV", "Order value from ads shown in search", "order", "sum", "", []map[string]any{
		{"field": "source", "op": "eq", "values": []string{"search"}}, {"field": "is_ads", "op": "eq", "values": []string{"true"}}}},
	{"search_buyers", "Search buyers", "1 if the unit ordered from search", "order", "any", "", []map[string]any{{"field": "source", "op": "eq", "values": []string{"search"}}}},
	{"total_gmv", "Total GMV", "All order value, any source", "order", "sum", "", nil},
}

var demoMetrics = []metricSpec{
	{"gmv_per_user", "Search GMV / User", "Search GMV per exposed user", "search_gmv / users", "currency", "increase", 2},
	{"gmv", "Search GMV", "Total search GMV (compared per user across variants)", "search_gmv", "currency", "increase", 0},
	{"ads_gmv", "Search Ads GMV", "GMV from search ads", "search_ads_gmv", "currency", "increase", 0},
	{"ctr", "CTR", "Clicks per impression", "search_clicks / search_impressions", "percent", "increase", 2},
	{"cvr", "CVR", "Orders per click", "search_orders / search_clicks", "percent", "increase", 2},
	{"buyer_rate", "Buyer rate", "Share of users who ordered from search", "search_buyers / users", "percent", "increase", 2},
	{"ads_share", "Ads share of GMV", "Share of search GMV from ads", "ads_gmv / gmv", "percent", "neutral", 1},
	{"aov", "Average order value", "Search GMV per search order", "search_gmv / search_orders", "currency", "increase", 2},
	{"searches_per_user", "Searches / User", "Search activity per user", "searches / users", "number", "increase", 2},
	{"search_gmv_share", "Search share of total GMV", "How much of all GMV comes through search", "search_gmv / total_gmv", "percent", "neutral", 1},
}

// Seed creates the demo business, metrics, layers and experiments. It
// returns the ids of the experiments it started.
func Seed(ctx context.Context, db *sql.DB, ownerID int64) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM businesses WHERE key = 'search'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrSeeded
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var bid int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO businesses (key, name, description, require_review) VALUES ('search', 'Search', 'Search results, ranking and search ads', true) RETURNING id`).Scan(&bid); err != nil {
		return err
	}
	for _, m := range demoMeasures {
		filters, _ := json.Marshal(orEmpty(m.filters))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO measures (business_id, key, name, description, event_name, aggregation, value_field, filters)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, bid, m.key, m.name, m.desc, m.event, m.agg, m.valueField, filters); err != nil {
			return fmt.Errorf("measure %s: %w", m.key, err)
		}
	}
	metricIDs := map[string]int64{}
	for _, m := range demoMetrics {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO metrics (business_id, key, name, description, formula, format, decimals, direction)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, bid, m.key, m.name, m.desc, m.formula, m.format, m.decimals, m.direction).Scan(&id); err != nil {
			return fmt.Errorf("metric %s: %w", m.key, err)
		}
		metricIDs[m.key] = id
	}
	groups := []struct {
		name, desc string
		keys       []string
	}{
		{"Search core", "The headline search metrics", []string{"gmv_per_user", "gmv", "ads_gmv", "ctr", "cvr"}},
		{"Search funnel", "From search to order", []string{"searches_per_user", "ctr", "cvr", "buyer_rate", "aov"}},
		{"Ads health", "Ads contribution and guardrails", []string{"ads_gmv", "ads_share", "gmv_per_user", "search_gmv_share"}},
	}
	groupIDs := map[string]int64{}
	for _, g := range groups {
		var ids []int64
		for _, k := range g.keys {
			ids = append(ids, metricIDs[k])
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO metric_groups (business_id, name, description, metric_ids) VALUES ($1, $2, $3, $4) RETURNING id`,
			bid, g.name, g.desc, ids).Scan(&id); err != nil {
			return err
		}
		groupIDs[g.name] = id
	}
	layerIDs := map[string]int64{}
	for _, l := range []struct{ name, desc, diversion string }{
		{"search_ranking", "Ranking and relevance experiments (mutually exclusive), split by user", assign.DiversionUser},
		{"search_ui", "Search result page layout and ads placement, split by device", assign.DiversionDevice},
	} {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO layers (name, description, salt, diversion) VALUES ($1, $2, $3, $4) RETURNING id`,
			l.name, l.desc, "demo-"+l.name, l.diversion).Scan(&id); err != nil {
			return err
		}
		layerIDs[l.name] = id
	}

	start := time.Now().UTC().AddDate(0, 0, -13)
	type variant struct {
		key, name string
		control   bool
		weight    int
		params    map[string]any
	}
	exps := []struct {
		name, hypothesis, layer, group, status string
		traffic                                int
		targeting                              assign.Targeting
		variants                               []variant
	}{
		{
			"Ranking formula v2", "Adding a price-competitiveness boost to the ranking formula raises CTR and CVR, lifting search GMV per user.",
			"search_ranking", "Search core", assign.StatusActive, 500,
			assign.Targeting{Groups: [][]assign.Rule{{{Attr: "region", Op: "in", Values: []string{"ID", "SG", "MY", "TH"}}}}},
			[]variant{
				{"control", "Current formula", true, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"formula": "ctr * cvr", "price_boost": 0}}}},
				{"treatment", "Price boost", false, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"formula": "ctr * cvr * price_score", "price_boost": 0.3}}}},
			},
		},
		{
			"Ads slot position", "Moving the first ad slot up raises ads GMV without hurting overall search GMV.",
			"search_ui", "Ads health", assign.StatusActive, 600, assign.Targeting{},
			[]variant{
				{"control", "Ad at slot 4", true, 340, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 4}}}},
				{"slot_2", "Ad at slot 2", false, 330, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 2}}}},
				{"slot_1", "Ad at slot 1", false, 330, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 1}}}},
			},
		},
		{
			"Query autocomplete", "Showing autocomplete suggestions increases searches per user.",
			"search_ranking", "Search funnel", assign.StatusDraft, 300, assign.Targeting{},
			[]variant{
				{"control", "No suggestions", true, 500, map[string]any{"search": map[string]any{"autocomplete": false}}},
				{"treatment", "Suggestions", false, 500, map[string]any{"search": map[string]any{"autocomplete": true}}},
			},
		},
	}
	for i, e := range exps {
		targeting, _ := json.Marshal(e.targeting)
		var id int64
		var owner any
		if ownerID > 0 {
			owner = ownerID
		}
		var buckets []int
		if e.status == assign.StatusActive {
			taken := map[int]bool{}
			rows, err := tx.QueryContext(ctx, `SELECT unnest(buckets) FROM experiments WHERE layer_id = $1`, layerIDs[e.layer])
			if err != nil {
				return err
			}
			for rows.Next() {
				var b int
				_ = rows.Scan(&b)
				taken[b] = true
			}
			rows.Close()
			buckets, err = assign.Allocate(nil, e.traffic, taken, fmt.Sprintf("demo-exp-%d", i))
			if err != nil {
				return err
			}
		}
		var startedAt any
		if e.status == assign.StatusActive {
			startedAt = start
		}
		if buckets == nil {
			buckets = []int{}
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO experiments (business_id, layer_id, name, hypothesis, owner_id, status, salt, traffic_target, buckets, targeting, metric_group_id, started_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
			bid, layerIDs[e.layer], e.name, e.hypothesis, owner, e.status, fmt.Sprintf("demo-exp-%d", i), e.traffic, buckets, targeting,
			groupIDs[e.group], startedAt, start.Add(-24*time.Hour)).Scan(&id); err != nil {
			return err
		}
		for pos, v := range e.variants {
			params, _ := json.Marshal(v.params)
			if _, err := tx.ExecContext(ctx, `INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				id, v.key, v.name, v.control, v.weight, params, pos); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (experiment_id, entity, entity_id, actor_id, action, to_status, detail) VALUES ($1, 'experiment', $1, $2, 'seed', $3, '{"note":"created by the demo seeder"}')`,
			id, owner, e.status); err != nil {
			return err
		}
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func orEmpty(f []map[string]any) []map[string]any {
	if f == nil {
		return []map[string]any{}
	}
	return f
}

// Options control traffic generation.
type Options struct {
	Business string // business key events are sent to (default "search")
	Users    int
	Days     int // days of history, ending today
	Seed     int64
}

// Stats summarizes what was generated.
type Stats struct {
	Users     int `json:"users"`
	Exposures int `json:"exposures"`
	Events    int `json:"events"`
}

type userProfile struct {
	id       string
	device   string
	attrs    map[string]any
	activity float64 // chance of a session on a given day
	ctr      float64
	cvr      float64
	aov      float64
}

var regions = []struct {
	code   string
	weight float64
}{{"ID", 0.45}, {"SG", 0.15}, {"MY", 0.15}, {"TH", 0.15}, {"VN", 0.10}}

// Generate simulates Days of traffic for Users units against the current
// snapshot, writing exposures and events straight to the raw tables.
func Generate(ctx context.Context, db *sql.DB, o Options) (Stats, error) {
	if o.Business == "" {
		o.Business = "search"
	}
	if o.Users <= 0 {
		o.Users = 20000
	}
	if o.Days <= 0 {
		o.Days = 14
	}
	if o.Days > 30 {
		o.Days = 30
	}
	var bid int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM businesses WHERE key = $1`, o.Business).Scan(&bid); err != nil {
		return Stats{}, fmt.Errorf("business %q: %w", o.Business, err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM config_version`).Scan(&version); err != nil {
		return Stats{}, err
	}
	snap, err := serving.Load(ctx, db, version)
	if err != nil {
		return Stats{}, err
	}
	rng := rand.New(rand.NewSource(o.Seed))
	users := make([]userProfile, o.Users)
	for i := range users {
		region := regions[len(regions)-1].code
		x := rng.Float64()
		for _, r := range regions {
			if x < r.weight {
				region = r.code
				break
			}
			x -= r.weight
		}
		os := "android"
		if rng.Float64() < 0.3 {
			os = "ios"
		}
		users[i] = userProfile{
			id:       fmt.Sprintf("demo-%s-%06d", o.Business, i),
			device:   fmt.Sprintf("dev-%s-%06d", o.Business, i),
			attrs:    map[string]any{"region": region, "os": os, "device": os, "app_version": fmt.Sprintf("10.%d.0", 1+rng.Intn(5))},
			activity: 0.15 + 0.5*rng.Float64(),
			ctr:      0.04 + 0.1*rng.Float64(),
			cvr:      0.04 + 0.12*rng.Float64(),
			aov:      math.Exp(3.0 + 0.5*rng.NormFloat64()),
		}
	}

	st := Stats{Users: o.Users}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	exposed := map[string]bool{}
	var exposures []serving.Exposure
	var events []serving.Event
	flush := func(force bool) error {
		if len(events) >= 5000 || (force && len(events) > 0) {
			if err := serving.WriteEvents(ctx, db, events); err != nil {
				return err
			}
			st.Events += len(events)
			events = events[:0]
		}
		if len(exposures) >= 5000 || (force && len(exposures) > 0) {
			if err := serving.WriteExposures(ctx, db, exposures); err != nil {
				return err
			}
			st.Exposures += len(exposures)
			exposures = exposures[:0]
		}
		return nil
	}
	for d := o.Days - 1; d >= 0; d-- {
		dayStart := today.AddDate(0, 0, -d)
		span := 24 * time.Hour
		if d == 0 {
			span = now.Sub(dayStart) - time.Minute
			if span < time.Minute {
				continue
			}
		}
		for i := range users {
			u := &users[i]
			if rng.Float64() > u.activity {
				continue
			}
			ts := dayStart.Add(time.Duration(rng.Int63n(int64(span))))
			res := snap.Resolve(assign.Request{UserID: u.id, DeviceID: u.device, Business: o.Business, Attrs: u.attrs}, false)
			fx := effects(res.Params, u.attrs)
			for _, h := range res.Hits {
				key := fmt.Sprintf("%d:%s", h.ExperimentID, h.UnitID)
				if h.Source == assign.SourceTraffic && !exposed[key] {
					exposed[key] = true
					exposures = append(exposures, serving.Exposure{
						ExperimentID: h.ExperimentID, VariantID: h.VariantID, UnitID: h.UnitID, UnitType: h.UnitType, TS: ts,
						Attrs: serving.DimensionAttrs(u.attrs),
					})
				}
			}
			searches := int(math.Round(float64(1+rng.Intn(4)) * fx.searches))
			for s := 0; s < searches; s++ {
				t := ts.Add(time.Duration(s) * 40 * time.Second)
				impressions := 10 + rng.Intn(11)
				props, _ := json.Marshal(map[string]any{"impressions": impressions, "query_len": 1 + rng.Intn(5)})
				events = append(events, serving.Event{BusinessID: bid, Name: "search", UnitID: u.id, DeviceID: u.device, TS: t, Props: props})
				ctr := math.Min(0.9, u.ctr*fx.ctr)
				cvr := math.Min(0.9, u.cvr*fx.cvr)
				for k := 0; k < impressions; k++ {
					if rng.Float64() >= ctr {
						continue
					}
					ct := t.Add(time.Duration(5+k) * time.Second)
					events = append(events, serving.Event{BusinessID: bid, Name: "search_click", UnitID: u.id, DeviceID: u.device, TS: ct, Props: []byte(`{"position":` + fmt.Sprint(k+1) + `}`)})
					if rng.Float64() < cvr {
						isAds := rng.Float64() < fx.adsShare
						value := math.Round(u.aov*math.Exp(0.3*rng.NormFloat64())*fx.aov*100) / 100
						p, _ := json.Marshal(map[string]any{"source": "search", "is_ads": isAds, "items": 1 + rng.Intn(3)})
						events = append(events, serving.Event{BusinessID: bid, Name: "order", UnitID: u.id, DeviceID: u.device, TS: ct.Add(2 * time.Minute), Value: value, Props: p})
					}
				}
			}
			// Orders from other surfaces, so "search" filters matter.
			if rng.Float64() < 0.08 {
				p, _ := json.Marshal(map[string]any{"source": "feed", "is_ads": false})
				events = append(events, serving.Event{BusinessID: bid, Name: "order", UnitID: u.id, DeviceID: u.device, TS: ts.Add(10 * time.Minute), Value: math.Round(u.aov*100) / 100, Props: p})
			}
			if err := flush(false); err != nil {
				return st, err
			}
		}
	}
	return st, flush(true)
}

type effect struct{ ctr, cvr, aov, adsShare, searches float64 }

// effects turns the served parameters into behaviour changes — the "true"
// treatment effects the reports should recover.
func effects(params map[string]any, attrs map[string]any) effect {
	fx := effect{ctr: 1, cvr: 1, aov: 1, adsShare: 0.22, searches: 1}
	search, _ := params["search"].(map[string]any)
	if ranking, ok := search["ranking"].(map[string]any); ok {
		if boost, _ := ranking["price_boost"].(float64); boost > 0 {
			fx.ctr *= 1.05
			fx.cvr *= 1.04
			fx.aov *= 0.99 // cheaper items rank higher
			if attrs["region"] == "ID" {
				fx.cvr *= 1.03 // stronger where price sensitivity is higher
			}
		}
	}
	if ads, ok := search["ads"].(map[string]any); ok {
		switch slot, _ := ads["first_slot"].(float64); slot {
		case 2:
			fx.adsShare, fx.ctr = 0.28, fx.ctr*0.99
		case 1:
			fx.adsShare, fx.ctr, fx.cvr = 0.34, fx.ctr*0.96, fx.cvr*0.98
		}
	}
	if ac, ok := search["autocomplete"].(bool); ok && ac {
		fx.searches = 1.2
	}
	return fx
}
