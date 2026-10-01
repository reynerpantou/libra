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

type groupSpec struct {
	name, desc string
	isDefault  bool
	keys       []string
}

// scopeSpec is a platform or a business with its definitions.
type scopeSpec struct {
	key, name, desc string
	measures        []measureSpec
	metrics         []metricSpec
	groups          []groupSpec
}

var searchOnly = []map[string]any{{"field": "source", "op": "eq", "values": []string{"search"}}}

// The demo platform: definitions shared by every business under it, and a
// default group of guardrails that applies to all of their experiments.
var demoPlatform = scopeSpec{
	key: "shop", name: "Demo Shop", desc: "Marketplace app: search, recommendations and checkout",
	measures: []measureSpec{
		{"total_gmv", "Total GMV", "All order value, any source", "order", "sum", "", nil},
		{"buyers", "Buyers", "1 if the unit ordered anything", "order", "any", "", nil},
		{"api_requests", "API requests", "Backend requests served", "api_request", "count", "", nil},
		{"api_latency", "API latency (sum)", "Total backend latency in ms", "api_request", "sum", "latency_ms", nil},
		{"api_errors", "API errors", "Backend requests that failed", "api_request", "count", "", []map[string]any{{"field": "error", "op": "eq", "values": []string{"true"}}}},
	},
	metrics: []metricSpec{
		{"gmv_per_user_total", "Total GMV / User", "All GMV per exposed user", "total_gmv / users", "currency", "increase", 2},
		{"conversion", "Conversion", "Share of users who ordered anything", "buyers / users", "percent", "increase", 2},
		{"avg_latency_ms", "Avg latency (ms)", "Mean backend latency per request", "api_latency / api_requests", "number", "decrease", 1},
		{"error_rate", "Error rate", "Failed backend requests", "api_errors / api_requests", "percent", "decrease", 3},
	},
	groups: []groupSpec{
		{"Default metrics", "Guardrails applied to every experiment on the platform", true, []string{"gmv_per_user_total", "conversion", "avg_latency_ms", "error_rate"}},
	},
}

var demoBusinesses = []scopeSpec{
	{
		key: "search", name: "Search", desc: "Search results, ranking and search ads",
		measures: []measureSpec{
			{"searches", "Searches", "Search queries issued", "search", "count", "", nil},
			{"search_impressions", "Search impressions", "Results shown (the impressions property of each search)", "search", "sum", "impressions", nil},
			{"search_clicks", "Search clicks", "Clicks on search results", "search_click", "count", "", nil},
			{"search_orders", "Search orders", "Orders attributed to search", "order", "count", "", searchOnly},
			{"search_gmv", "Search GMV", "Order value attributed to search", "order", "sum", "", searchOnly},
			{"search_ads_gmv", "Search ads GMV", "Order value from ads shown in search", "order", "sum", "", []map[string]any{
				{"field": "source", "op": "eq", "values": []string{"search"}}, {"field": "is_ads", "op": "eq", "values": []string{"true"}}}},
			{"search_buyers", "Search buyers", "1 if the unit ordered from search", "order", "any", "", searchOnly},
			{"search_latency", "Search latency (sum)", "Search backend time in ms", "search", "sum", "latency_ms", nil},
			{"zero_result_searches", "Zero-result searches", "Searches that found nothing", "search", "count", "", []map[string]any{{"field": "zero_result", "op": "eq", "values": []string{"true"}}}},
		},
		metrics: []metricSpec{
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
			{"search_latency_ms", "Search latency (ms)", "Mean search backend time", "search_latency / searches", "number", "decrease", 1},
			{"zero_result_rate", "Zero-result rate", "Searches that found nothing", "zero_result_searches / searches", "percent", "decrease", 2},
		},
		groups: []groupSpec{
			{"Back end", "Search service health", false, []string{"search_latency_ms", "zero_result_rate", "avg_latency_ms", "error_rate"}},
			{"Front end", "Engagement on the result page", false, []string{"ctr", "searches_per_user", "buyer_rate"}},
			{"Product", "The headline search business metrics", false, []string{"gmv_per_user", "gmv", "ads_gmv", "cvr", "aov"}},
			{"Ads health", "Ads contribution and guardrails", false, []string{"ads_gmv", "ads_share", "gmv_per_user", "search_gmv_share"}},
		},
	},
	{
		key: "reco", name: "Recommendation", desc: "Home feed and \"you may also like\" recommendations",
		measures: []measureSpec{
			{"feed_views", "Feed views", "Feed screens shown", "feed_view", "count", "", nil},
			{"feed_impressions", "Feed impressions", "Items shown in the feed", "feed_view", "sum", "items", nil},
			{"feed_clicks", "Feed clicks", "Clicks on feed items", "feed_click", "count", "", nil},
			{"feed_gmv", "Feed GMV", "Order value attributed to the feed", "order", "sum", "", []map[string]any{{"field": "source", "op": "eq", "values": []string{"feed"}}}},
		},
		metrics: []metricSpec{
			{"feed_ctr", "Feed CTR", "Clicks per feed impression", "feed_clicks / feed_impressions", "percent", "increase", 2},
			{"feed_gmv_per_user", "Feed GMV / User", "Feed GMV per exposed user", "feed_gmv / users", "currency", "increase", 2},
			{"feed_views_per_user", "Feed views / User", "Feed activity per user", "feed_views / users", "number", "increase", 2},
		},
		groups: []groupSpec{
			{"Product", "Headline recommendation metrics", false, []string{"feed_ctr", "feed_gmv_per_user", "feed_views_per_user"}},
		},
	},
}

// insertScope writes a scope's definitions; metric and group ids are added
// to the maps under "<scope key>/<key or name>". Metric keys resolve in the
// scope first, then on the platform.
func insertScope(ctx context.Context, tx *sql.Tx, sc scopeSpec, businessID, platformID any, metricIDs, groupIDs map[string]int64) error {
	for _, m := range sc.measures {
		filters, _ := json.Marshal(orEmpty(m.filters))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO measures (business_id, platform_id, key, name, description, event_name, aggregation, value_field, filters)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, businessID, platformID, m.key, m.name, m.desc, m.event, m.agg, m.valueField, filters); err != nil {
			return fmt.Errorf("measure %s: %w", m.key, err)
		}
	}
	for _, m := range sc.metrics {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO metrics (business_id, platform_id, key, name, description, formula, format, decimals, direction)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`, businessID, platformID, m.key, m.name, m.desc, m.formula, m.format, m.decimals, m.direction).Scan(&id); err != nil {
			return fmt.Errorf("metric %s: %w", m.key, err)
		}
		metricIDs[sc.key+"/"+m.key] = id
	}
	if platformID != nil {
		// A platform created in the UI already has an empty default group.
		if _, err := tx.ExecContext(ctx, `DELETE FROM metric_groups WHERE platform_id = $1 AND builtin AND metric_ids = '{}'`, platformID); err != nil {
			return err
		}
	}
	for _, g := range sc.groups {
		ids := []int64{}
		for _, k := range g.keys {
			id, ok := metricIDs[sc.key+"/"+k]
			if !ok {
				id = metricIDs[demoPlatform.key+"/"+k]
			}
			ids = append(ids, id)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO metric_groups (business_id, platform_id, name, description, is_default, builtin, metric_ids) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			businessID, platformID, g.name, g.desc, g.isDefault, g.isDefault && platformID != nil, ids).Scan(&id); err != nil {
			return err
		}
		groupIDs[sc.key+"/"+g.name] = id
	}
	return nil
}

// Seed creates the demo platform, its businesses, metrics, layers and
// experiments.
func Seed(ctx context.Context, db *sql.DB, ownerID int64) error {
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE p.key = $1 AND b.key IN ('search', 'reco')`, demoPlatform.key).Scan(&n); err != nil {
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

	var pid int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM platforms WHERE key = $1`, demoPlatform.key).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO platforms (key, name, description) VALUES ($1, $2, $3) RETURNING id`,
			demoPlatform.key, demoPlatform.name, demoPlatform.desc).Scan(&pid)
	}
	if err != nil {
		return err
	}
	metricIDs, groupIDs := map[string]int64{}, map[string]int64{}
	if err := insertScope(ctx, tx, demoPlatform, nil, pid, metricIDs, groupIDs); err != nil {
		return err
	}
	bids := map[string]int64{}
	for _, b := range demoBusinesses {
		var bid int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO businesses (platform_id, key, name, description, require_review) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			pid, b.key, b.name, b.desc, b.key == "search").Scan(&bid); err != nil {
			return err
		}
		bids[b.key] = bid
		if err := insertScope(ctx, tx, b, bid, nil, metricIDs, groupIDs); err != nil {
			return err
		}
	}
	layerIDs := map[string]int64{}
	for _, l := range []struct{ name, desc, diversion string }{
		{"search_ranking", "Ranking and relevance experiments (mutually exclusive), split by user", assign.DiversionUser},
		{"search_ui", "Search result page layout and ads placement, split by device", assign.DiversionDevice},
		{"feed_ranking", "Recommendation models, split by user", assign.DiversionUser},
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
	card := func(layout string, cols int) map[string]any {
		return map[string]any{"search": map[string]any{"card": map[string]any{"layout": layout, "columns": cols}}}
	}
	exps := []struct {
		business, name, hypothesis, layer, status string
		auto                                      string // diversion of a dedicated (auto) layer
		launch                                    string // for launched experiments: the winning variant
		groups                                    []string
		traffic                                   int
		targeting                                 assign.Targeting
		variants                                  []variant
	}{
		{
			"search", "Ranking formula v2", "Adding a price-competitiveness boost to the ranking formula raises CTR and CVR, lifting search GMV per user.",
			"search_ranking", assign.StatusActive, "", "", []string{"search/Product", "search/Back end"}, 500,
			assign.Targeting{Groups: [][]assign.Rule{{{Attr: "region", Op: "in", Values: []string{"ID", "SG", "MY", "TH"}}}}},
			[]variant{
				{"control", "Current formula", true, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"formula": "ctr * cvr", "price_boost": 0}}}},
				{"treatment", "Price boost", false, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"formula": "ctr * cvr * price_score", "price_boost": 0.3}}}},
			},
		},
		{
			"search", "Ads slot position", "Moving the first ad slot up raises ads GMV without hurting overall search GMV.",
			"search_ui", assign.StatusActive, "", "", []string{"search/Ads health"}, 600, assign.Targeting{},
			[]variant{
				{"control", "Ad at slot 4", true, 340, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 4}}}},
				{"slot_2", "Ad at slot 2", false, 330, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 2}}}},
				{"slot_1", "Ad at slot 1", false, 330, map[string]any{"search": map[string]any{"ads": map[string]any{"first_slot": 1}}}},
			},
		},
		{
			"search", "Result card layout", "One of the new result card layouts lifts CTR; the dense ones may hurt conversion.",
			"", assign.StatusActive, assign.DiversionDevice, "", []string{"search/Front end", "search/Product", "reco/Product"}, 1000, assign.Targeting{},
			[]variant{
				{"control", "List (current)", true, 125, card("list", 1)},
				{"grid_2", "Grid, 2 columns", false, 125, card("grid", 2)},
				{"grid_3", "Grid, 3 columns", false, 125, card("grid", 3)},
				{"large_image", "Large image", false, 125, card("large_image", 1)},
				{"compact", "Compact list", false, 125, card("compact", 1)},
				{"price_first", "Price first", false, 125, card("price_first", 1)},
				{"badges", "With badges", false, 125, card("badges", 1)},
				{"video", "Video preview", false, 125, card("video", 1)},
			},
		},
		{
			"reco", "Feed model two-tower", "A two-tower retrieval model raises feed CTR and GMV at a small latency cost.",
			"feed_ranking", assign.StatusActive, "", "", []string{"reco/Product"}, 800, assign.Targeting{},
			[]variant{
				{"control", "Current model", true, 500, map[string]any{"reco": map[string]any{"model": "gbdt_v7", "candidates": 200}}},
				{"two_tower", "Two-tower", false, 500, map[string]any{"reco": map[string]any{"model": "two_tower_v1", "candidates": 400}}},
			},
		},
		{
			"search", "Results per page", "Showing 30 results per page instead of 20 raises clicks without slowing search.",
			"search_ui", assign.StatusLaunched, "", "thirty", []string{"search/Front end"}, 0, assign.Targeting{},
			[]variant{
				{"control", "20 per page", true, 500, map[string]any{"search": map[string]any{"page_size": 20, "ads": map[string]any{"max_per_page": 3}}}},
				{"thirty", "30 per page", false, 500, map[string]any{"search": map[string]any{"page_size": 30, "ads": map[string]any{"max_per_page": 4}}}},
			},
		},
		{
			"search", "Ads per page v1", "Capping ads at 2 per page and labelling them keeps CTR healthy.",
			"search_ui", assign.StatusLaunched, "", "capped", []string{"search/Ads health"}, 0, assign.Targeting{},
			[]variant{
				{"control", "No cap", true, 500, map[string]any{"search": map[string]any{"ads": map[string]any{"max_per_page": 6, "label": "Ad"}}}},
				{"capped", "2 per page, labelled", false, 500, map[string]any{"search": map[string]any{"ads": map[string]any{"max_per_page": 2, "label": "Sponsored"}}}},
			},
		},
		{
			"search", "Legacy ranking weights", "Old ranking weights, launched and later retired.",
			"search_ranking", assign.StatusArchived, "", "tuned", []string{"search/Product"}, 0, assign.Targeting{},
			[]variant{
				{"control", "Defaults", true, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"weights": map[string]any{"text": 1.0, "sales": 0.5}}}}},
				{"tuned", "Tuned", false, 500, map[string]any{"search": map[string]any{"ranking": map[string]any{"weights": map[string]any{"text": 0.8, "sales": 0.7}}}}},
			},
		},
		{
			"reco", "Price badge on feed cards", "A price-drop badge on feed cards raises feed CTR.",
			"feed_ranking", assign.StatusLaunched, "", "badge", []string{"reco/Product"}, 0,
			assign.Targeting{Groups: [][]assign.Rule{{{Attr: "region", Op: "in", Values: []string{"ID", "TH"}}}}},
			[]variant{
				{"control", "No badge", true, 500, map[string]any{"reco": map[string]any{"card": map[string]any{"price_badge": false}}}},
				{"badge", "Price-drop badge", false, 500, map[string]any{"reco": map[string]any{"card": map[string]any{"price_badge": true, "badge_color": "red"}}}},
			},
		},
		{
			"search", "Query autocomplete", "Showing autocomplete suggestions increases searches per user.",
			"search_ranking", assign.StatusDraft, "", "", []string{"search/Front end"}, 300, assign.Targeting{},
			[]variant{
				{"control", "No suggestions", true, 500, map[string]any{"search": map[string]any{"autocomplete": false}}},
				{"treatment", "Suggestions", false, 500, map[string]any{"search": map[string]any{"autocomplete": true}}},
			},
		},
	}
	expIDs := map[string]int64{}
	for i, e := range exps {
		targeting, _ := json.Marshal(e.targeting)
		var id int64
		var owner any
		if ownerID > 0 {
			owner = ownerID
		}
		layerID := layerIDs[e.layer]
		if e.auto != "" {
			if err := tx.QueryRowContext(ctx, `INSERT INTO layers (name, description, salt, diversion, auto) VALUES ($1, 'Dedicated layer', $2, $3, true) RETURNING id`,
				fmt.Sprintf("auto-demo-%d", i), fmt.Sprintf("demo-auto-%d", i), e.auto).Scan(&layerID); err != nil {
				return err
			}
		}
		var buckets []int
		if e.status == assign.StatusActive {
			taken := map[int]bool{}
			rows, err := tx.QueryContext(ctx, `SELECT unnest(buckets) FROM experiments WHERE layer_id = $1`, layerID)
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
		groups := []int64{}
		for _, g := range e.groups {
			groups = append(groups, groupIDs[g])
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO experiments (business_id, layer_id, name, hypothesis, owner_id, status, salt, traffic_target, buckets, targeting, metric_group_ids, started_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
			bids[e.business], layerID, e.name, e.hypothesis, owner, e.status, fmt.Sprintf("demo-exp-%d", i), e.traffic, buckets, targeting,
			groups, startedAt, start.Add(-24*time.Hour)).Scan(&id); err != nil {
			return err
		}
		expIDs[e.name] = id
		if e.auto != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE layers SET name = $2 WHERE id = $1`, layerID, fmt.Sprintf("auto-%d", id)); err != nil {
				return err
			}
		}
		for pos, v := range e.variants {
			params, _ := json.Marshal(map[string]any{demoPlatform.key: v.params}) // namespaced by platform
			var vid int64
			if err := tx.QueryRowContext(ctx, `INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
				id, v.key, v.name, v.control, v.weight, params, pos).Scan(&vid); err != nil {
				return err
			}
			if e.launch == v.key {
				if _, err := tx.ExecContext(ctx, `UPDATE experiments SET launched_variant_id = $2, started_at = $3, ended_at = $4, launched_at = $4 WHERE id = $1`,
					id, vid, start.AddDate(0, 0, -30), start.AddDate(0, 0, -16)); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (experiment_id, entity, entity_id, actor_id, action, to_status, detail) VALUES ($1, 'experiment', $1, $2, 'seed', $3, '{"note":"created by the demo seeder"}')`,
			id, owner, e.status); err != nil {
			return err
		}
	}
	// Launch history: when each was released, and rollouts in progress —
	// a launch still ramping to everyone, and a running experiment whose
	// traffic ramps up step by step.
	for _, l := range []struct {
		name    string
		daysAgo int
		rollout int
	}{{"Legacy ranking weights", 60, 1000}, {"Ads per page v1", 40, 1000}, {"Results per page", 16, 1000}, {"Price badge on feed cards", 2, 300}} {
		at := time.Now().UTC().AddDate(0, 0, -l.daysAgo)
		if _, err := tx.ExecContext(ctx, `UPDATE experiments SET launched_at = $2, ended_at = $2, started_at = $3, launch_rollout = $4 WHERE id = $1`,
			expIDs[l.name], at, at.AddDate(0, 0, -14), l.rollout); err != nil {
			return err
		}
	}
	var by any
	if ownerID > 0 {
		by = ownerID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rollouts (experiment_id, kind, start_value, target, step, interval_secs, next_at, note, created_by)
		VALUES ($1, 'launch', 100, 1000, 100, 3600, now() + interval '40 minutes', 'at 30.0%', $3),
		       ($2, 'traffic', 600, 1000, 50, 7200, now() + interval '75 minutes', 'at 80.0%', $3)`,
		expIDs["Price badge on feed cards"], expIDs["Feed model two-tower"], by); err != nil {
		return err
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
	Platform string // platform key (default "shop")
	Business string // business key events are sent to (default "search")
	Users    int
	Days     int // days of history, ending today
	Seed     int64
	// Start, when set, simulates Days whole days from this UTC midnight
	// instead of the days ending today.
	Start time.Time
	// UserPrefix names the simulated units (default "demo-<business>").
	UserPrefix string
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
	if o.Platform == "" {
		o.Platform = demoPlatform.key
	}
	const bizQuery = `SELECT b.id FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE p.key = $1 AND b.key = $2`
	if err := db.QueryRowContext(ctx, bizQuery, o.Platform, o.Business).Scan(&bid); err != nil {
		return Stats{}, fmt.Errorf("business %s/%s: %w", o.Platform, o.Business, err)
	}
	// Feed activity goes to the demo recommendation business when it's
	// there (and the simulation runs for search); otherwise it stays in bid.
	feedBID := bid
	if o.Business == "search" {
		_ = db.QueryRowContext(ctx, bizQuery, o.Platform, "reco").Scan(&feedBID)
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
	if o.UserPrefix == "" {
		o.UserPrefix = "demo-" + o.Business
	}
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
			id:       fmt.Sprintf("%s-%06d", o.UserPrefix, i),
			device:   fmt.Sprintf("dev-%s-%06d", o.UserPrefix[min(len(o.UserPrefix), 5):], i),
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
		if !o.Start.IsZero() {
			dayStart = o.Start.UTC().AddDate(0, 0, o.Days-1-d)
		}
		span := 24 * time.Hour
		if !dayStart.Add(span).Before(now) {
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
			business := o.Business
			if feedBID != bid {
				business = "" // search and reco experiments
			}
			res := snap.Resolve(assign.Request{UserID: u.id, DeviceID: u.device, Platform: o.Platform, Business: business, Attrs: u.attrs}, false)
			ours, _ := res.Params[o.Platform].(map[string]any) // this platform's namespace
			fx := effects(ours, u.attrs)
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
				zero := rng.Float64() < 0.06*fx.zero
				if zero {
					impressions = 0
				}
				latency := math.Round(fx.latency * (80 + 40*rng.ExpFloat64()))
				props, _ := json.Marshal(map[string]any{"impressions": impressions, "query_len": 1 + rng.Intn(5), "latency_ms": latency, "zero_result": zero})
				events = append(events, serving.Event{BusinessID: bid, Name: "search", UnitID: u.id, DeviceID: u.device, TS: t, Props: props})
				events = append(events, apiRequest(rng, bid, u, t, latency, fx.errors))
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
			// Feed browsing and its orders, so "search" filters matter and
			// the platform totals span businesses.
			views := int(math.Round(float64(rng.Intn(3)) * fx.feedViews))
			for v := 0; v < views; v++ {
				t := ts.Add(time.Duration(30+v*50) * time.Minute)
				items := 8 + rng.Intn(9)
				latency := math.Round(fx.feedLatency * (60 + 30*rng.ExpFloat64()))
				p, _ := json.Marshal(map[string]any{"items": items, "latency_ms": latency})
				events = append(events, serving.Event{BusinessID: feedBID, Name: "feed_view", UnitID: u.id, DeviceID: u.device, TS: t, Props: p})
				events = append(events, apiRequest(rng, feedBID, u, t, latency, 1))
				for k := 0; k < items; k++ {
					if rng.Float64() >= 0.03*fx.feedCTR {
						continue
					}
					events = append(events, serving.Event{BusinessID: feedBID, Name: "feed_click", UnitID: u.id, DeviceID: u.device, TS: t.Add(time.Duration(k) * time.Second), Props: []byte(`{}`)})
					if rng.Float64() < 0.06*fx.feedCVR {
						p, _ := json.Marshal(map[string]any{"source": "feed", "is_ads": false})
						events = append(events, serving.Event{BusinessID: feedBID, Name: "order", UnitID: u.id, DeviceID: u.device, TS: t.Add(3 * time.Minute), Value: math.Round(u.aov*math.Exp(0.3*rng.NormFloat64())*100) / 100, Props: p})
					}
				}
			}
			if err := flush(false); err != nil {
				return st, err
			}
		}
	}
	return st, flush(true)
}

func apiRequest(rng *rand.Rand, bid int64, u *userProfile, t time.Time, latency, errors float64) serving.Event {
	p, _ := json.Marshal(map[string]any{"latency_ms": latency, "error": rng.Float64() < 0.004*errors})
	return serving.Event{BusinessID: bid, Name: "api_request", UnitID: u.id, DeviceID: u.device, TS: t, Props: p}
}

type effect struct {
	ctr, cvr, aov, adsShare, searches float64
	latency, zero, errors             float64
	feedViews, feedCTR, feedCVR       float64
	feedLatency                       float64
}

// effects turns the served parameters into behaviour changes — the "true"
// treatment effects the reports should recover.
func effects(params map[string]any, attrs map[string]any) effect {
	fx := effect{ctr: 1, cvr: 1, aov: 1, adsShare: 0.22, searches: 1, latency: 1, zero: 1, errors: 1, feedViews: 1, feedCTR: 1, feedCVR: 1, feedLatency: 1}
	search, _ := params["search"].(map[string]any)
	if ranking, ok := search["ranking"].(map[string]any); ok {
		if boost, _ := ranking["price_boost"].(float64); boost > 0 {
			fx.ctr *= 1.05
			fx.cvr *= 1.04
			fx.aov *= 0.99 // cheaper items rank higher
			fx.latency *= 1.08
			fx.zero *= 0.9
			if attrs["region"] == "ID" {
				fx.cvr *= 1.03 // stronger where price sensitivity is higher
			}
		}
	}
	// AB Tuning demo: two ranking weights with a smooth true optimum near
	// relevance 0.72 / freshness 0.70. Freshness costs latency (≈ +4% per
	// +0.1), so a 5% latency guardrail caps it near 0.62.
	if ranking, ok := search["ranking"].(map[string]any); ok {
		rel, okR := ranking["relevance_weight"].(float64)
		fresh, okF := ranking["freshness_weight"].(float64)
		if okR && okF {
			bump := 0.10 * math.Exp(-(math.Pow(rel-0.72, 2)/0.03 + math.Pow(fresh-0.70, 2)/0.05))
			fx.ctr *= 1 + 1.5*bump
			fx.cvr *= 1 + 0.3*bump
			fx.latency *= 1 + 0.4*(fresh-0.5)
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
	if c, ok := search["card"].(map[string]any); ok {
		// Each layout has its own true effect on CTR / CVR (and latency for
		// the heavy ones).
		switch c["layout"] {
		case "grid":
			if cols, _ := c["columns"].(float64); cols >= 3 {
				fx.ctr, fx.cvr = fx.ctr*1.06, fx.cvr*0.95
			} else {
				fx.ctr *= 1.03
			}
		case "large_image":
			fx.ctr, fx.cvr, fx.latency = fx.ctr*1.04, fx.cvr*1.02, fx.latency*1.05
		case "compact":
			fx.ctr, fx.searches = fx.ctr*0.97, fx.searches*1.05
		case "price_first":
			fx.cvr, fx.aov = fx.cvr*1.04, fx.aov*0.97
		case "badges":
			fx.ctr, fx.feedCTR = fx.ctr*1.02, fx.feedCTR*1.02
		case "video":
			fx.ctr, fx.cvr, fx.latency, fx.errors = fx.ctr*1.08, fx.cvr*1.01, fx.latency*1.25, fx.errors*2
		}
	}
	if reco, ok := params["reco"].(map[string]any); ok && reco["model"] == "two_tower_v1" {
		fx.feedCTR, fx.feedCVR, fx.feedViews, fx.feedLatency = fx.feedCTR*1.07, fx.feedCVR*1.03, fx.feedViews*1.04, fx.feedLatency*1.15
	}
	return fx
}
