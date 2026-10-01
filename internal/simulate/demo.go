package simulate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/tuning"
)

// The demo team: reviewers to invite, owners to filter by, roles to try.
var demoUsers = []struct{ username, email, name, role string }{
	{"bob", "bob@demo.libra", "Bob Santoso", "admin"},
	{"alice", "alice@demo.libra", "Alice Tan", "editor"},
	{"dina", "dina@demo.libra", "Dina Pratama", "editor"},
	{"evan", "evan@demo.libra", "Evan Lim", "editor"},
	{"carol", "carol@demo.libra", "Carol Wijaya", "viewer"},
}

// EnsureDemoUsers adds the demo team (existing accounts are kept).
func EnsureDemoUsers(ctx context.Context, db *sql.DB) error {
	for _, u := range demoUsers {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (username, email, display_name, role) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			u.username, u.email, u.name, u.role); err != nil {
			return err
		}
	}
	return nil
}

func demoUserIDs(ctx context.Context, db *sql.DB) map[string]int64 {
	out := map[string]int64{}
	for _, u := range demoUsers {
		var id int64
		if db.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(username) = $1`, u.username).Scan(&id) == nil {
			out[u.username] = id
		}
	}
	return out
}

// seedMarket adds the "Market App" platform: the same metric definitions as
// Demo Shop under its own key, a "search" business (the same key as Demo
// Shop's) and a "promo" business.
func seedMarket(ctx context.Context, tx *sql.Tx, bids map[string]int64, metricIDs, groupIDs map[string]int64) error {
	var pid int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO platforms (key, name, description) VALUES ('market', 'Market App', 'A second marketplace app — it also has a "search" business') RETURNING id`).Scan(&pid); err != nil {
		return err
	}
	mm, mg := map[string]int64{}, map[string]int64{}
	if err := insertScope(ctx, tx, demoPlatform, nil, pid, mm, mg); err != nil {
		return err
	}
	promo := scopeSpec{
		key: "promo", name: "Promotions", desc: "Vouchers and flash sales",
		measures: []measureSpec{
			{"promo_views", "Promo views", "Promotion banners seen", "promo_view", "count", "", nil},
			{"promo_claims", "Voucher claims", "Vouchers claimed", "promo_claim", "count", "", nil},
		},
		metrics: []metricSpec{
			{"claim_rate", "Claim rate", "Claims per promo view", "promo_claims / promo_views", "percent", "increase", 2},
		},
		groups: []groupSpec{{"Product", "Promotion health", false, []string{"claim_rate"}}},
	}
	for _, b := range []scopeSpec{demoBusinesses[0], promo} {
		var bid int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO businesses (platform_id, key, name, description) VALUES ($1, $2, $3, $4) RETURNING id`,
			pid, b.key, b.name, b.desc+" (Market App)").Scan(&bid); err != nil {
			return err
		}
		bids["market/"+b.key] = bid
		if err := insertScope(ctx, tx, b, bid, nil, mm, mg); err != nil {
			return err
		}
	}
	for k, v := range mm {
		metricIDs["market/"+k] = v
	}
	for k, v := range mg {
		groupIDs["market/"+k] = v
	}
	return nil
}

// applyExtras adds what an experiment needs beyond its row: reviewers and
// decisions, test users, extra businesses and other owners.
func applyExtras(ctx context.Context, tx *sql.Tx, id int64, name string, owner any, users map[string]int64, bids map[string]int64) error {
	variant := func(key string) int64 {
		var v int64
		_ = tx.QueryRowContext(ctx, `SELECT id FROM variants WHERE experiment_id = $1 AND key = $2`, id, key).Scan(&v)
		return v
	}
	whitelist := func(entries ...[3]string) error {
		for _, e := range entries {
			if _, err := tx.ExecContext(ctx, `INSERT INTO whitelist (experiment_id, unit_id, variant_id, note) VALUES ($1, $2, $3, $4)`, id, e[0], variant(e[1]), e[2]); err != nil {
				return err
			}
		}
		return nil
	}
	review := func(user, decision, note string) error {
		uid, ok := users[user]
		if !ok {
			return nil
		}
		var dec, at any
		if decision != "" {
			dec, at = decision, time.Now().UTC().Add(-3*time.Hour)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO experiment_reviewers (experiment_id, user_id, invited_by, decision, note, decided_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, uid, owner, dec, note, at)
		if err == nil && decision != "" {
			_, err = tx.ExecContext(ctx, `UPDATE experiments SET reviewer_id = $2, review_note = $3 WHERE id = $1`, id, uid, note)
		}
		return err
	}
	// event audits a lifecycle step by a demo user; the audit trail turns it
	// into notifications (owner, reviewers).
	event := func(action, by string, detail string) error {
		var actor any
		if uid, ok := users[by]; ok {
			actor = uid
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_log (experiment_id, entity, entity_id, actor_id, action, detail) VALUES ($1, 'experiment', $1, $2, $3, $4)`,
			id, actor, action, detail)
		return err
	}
	schedule := func(start, end *time.Time) error {
		_, err := tx.ExecContext(ctx, `UPDATE experiments SET planned_start = $2, end_at = $3 WHERE id = $1`, id, start, end)
		return err
	}
	at := func(d time.Duration) *time.Time { t := time.Now().UTC().Add(d); return &t }
	ownedBy := func(user string) error {
		if uid, ok := users[user]; ok {
			_, err := tx.ExecContext(ctx, `UPDATE experiments SET owner_id = $2 WHERE id = $1`, id, uid)
			return err
		}
		return nil
	}
	switch name {
	case "Ranking formula v2":
		// Ends within a day: its owner gets the "ends soon" reminder.
		if err := schedule(nil, at(20*time.Hour)); err != nil {
			return err
		}
		return whitelist([3]string{"demo-search-000001", "treatment", "QA phone — always the price boost"}, [3]string{"demo-search-000002", "control", "QA phone — always control"})
	case "Result card layout":
		return whitelist([3]string{"dev-search-000010", "video", "Design review device"}, [3]string{"dev-search-000011", "grid_3", "Design review device"})
	case "Voice search entry":
		if err := ownedBy("alice"); err != nil {
			return err
		}
		if err := review("dina", "", ""); err != nil {
			return err
		}
		if err := review("bob", "", ""); err != nil {
			return err
		}
		if err := schedule(at(4*24*time.Hour), at(18*24*time.Hour)); err != nil {
			return err
		}
		return event("submit", "alice", `{"reviewers": ["Dina Pratama", "Bob Santoso"]}`)
	case "Image search":
		if err := ownedBy("evan"); err != nil {
			return err
		}
		if err := review("bob", "approved", "Looks good — start at 10% and watch latency."); err != nil {
			return err
		}
		// Approved and past its planned start: "time to start".
		if err := schedule(at(-20*time.Hour), at(14*24*time.Hour)); err != nil {
			return err
		}
		return event("approve", "bob", `{"note": "Looks good — start at 10% and watch latency."}`)
	case "Infinite scroll":
		if err := ownedBy("alice"); err != nil {
			return err
		}
		if err := review("bob", "rejected", "Clashes with ads pagination. Split the ads change into its own test first."); err != nil {
			return err
		}
		return event("reject", "bob", `{"note": "Clashes with ads pagination. Split the ads change into its own test first."}`)
	case "Spelling correction":
		return ownedBy("dina")
	case "Unified ranking signals":
		_, err := tx.ExecContext(ctx, `UPDATE experiments SET business_ids = ARRAY[$2::bigint, $3::bigint] WHERE id = $1`, id, bids["search"], bids["reco"])
		return err
	case "Seller coupon nudge":
		if err := ownedBy("evan"); err != nil {
			return err
		}
		return schedule(at(3*24*time.Hour), at(31*24*time.Hour))
	case "Ads slot position", "Feed model two-tower":
		return schedule(nil, at(10*24*time.Hour))
	case "Price badge on feed cards":
		return event("launch", "bob", `{"variant": "badge", "rollout": "30.0%"}`)
	}
	return nil
}

// Finalize moves experiments that collected data into their final state:
// one paused (keeps its traffic), one stopped (report frozen).
func Finalize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, f := range []struct{ name, to, sets string }{
		{"Spelling correction", "paused", ""},
		{"Search filter chips", "stopped", ", buckets = '{}', ended_at = now() - interval '1 day'"},
	} {
		var id int64
		if err := tx.QueryRowContext(ctx, `UPDATE experiments SET status = $2, updated_at = now()`+f.sets+` WHERE name = $1 AND status = 'active' RETURNING id`, f.name, f.to).Scan(&id); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (experiment_id, entity, entity_id, action, from_status, to_status, detail) VALUES ($1, 'experiment', $1, $2, 'active', $3, '{"note":"demo"}')`,
			id, map[string]string{"paused": "pause", "stopped": "stop"}[f.to], f.to); err != nil {
			return err
		}
	}
	if err := serving.Bump(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DemoOptions size the full demo.
type DemoOptions struct {
	Users int // simulated users on Demo Shop (Market App gets a third)
	Days  int
	Seed  int64
	Log   func(format string, args ...any)
}

// FullDemo builds a demo of every feature on an empty database: the demo
// team, two platforms (both with a "search" business), custom diversions,
// shared and dedicated layers, experiments in every state (reviews,
// rejections, test users, a multi-business experiment, launches and
// rollouts in progress), simulated traffic and reports, and two AB Tuning
// studies — one running, one finished.
func FullDemo(ctx context.Context, db *sql.DB, ownerID int64, o DemoOptions) error {
	if o.Users <= 0 {
		o.Users = 20000
	}
	if o.Days <= 0 {
		o.Days = 14
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if err := EnsureDemoUsers(ctx, db); err != nil {
		return err
	}
	if err := Seed(ctx, db, ownerID); err != nil {
		return err
	}
	o.Log("seeded 2 platforms, 5 businesses, metrics and groups, 4 diversions, 5 layers, 17 experiments and the demo team")
	step := func(label string, opts Options) error {
		st, err := Generate(ctx, db, opts)
		if err != nil {
			return err
		}
		o.Log("%s: %d users, %d exposures, %d events", label, st.Users, st.Exposures, st.Events)
		return nil
	}
	if err := step("Demo Shop traffic", Options{Platform: "shop", Business: "search", Users: o.Users, Days: o.Days, Seed: o.Seed}); err != nil {
		return err
	}
	if err := step("Market App traffic", Options{Platform: "market", Business: "search", Users: max(1000, o.Users/3), Days: o.Days, Seed: o.Seed + 1, UserPrefix: "mk-search"}); err != nil {
		return err
	}
	ps, err := pipeline.RunSettled(ctx, db, "demo", 0)
	if err != nil {
		return err
	}
	o.Log("pipeline: %d assignments, %d measure rows in %.1fs", ps.Assignments, ps.Rows, ps.Seconds)
	if err := Finalize(ctx, db); err != nil {
		return err
	}
	if ownerID == 0 {
		ownerID = demoUserIDs(ctx, db)["bob"]
	}
	if _, err := SeedTuning(ctx, db, ownerID, TuningOptions{Users: o.Users, Rounds: 6, MaxRounds: 10, Seed: o.Seed + 2, Log: o.Log}); err != nil {
		return fmt.Errorf("tuning study: %w", err)
	}
	if _, err := SeedTuning(ctx, db, ownerID, TuningOptions{
		Name:        "Result page size",
		Hypothesis:  "Around 30 results per page is the sweet spot between clicks and latency.",
		Description: "One integer parameter, quasi-random search, finished — ready to launch.",
		Algorithm:   tuning.QuasiRandom,
		Space:       tuning.Space{{Path: "search.results.page_size", Type: "int", Min: 10, Max: 60, Control: 20}},
		Arms:        8,
		Users:       max(2000, o.Users*3/5),
		Rounds:      4,
		MaxRounds:   4,
		Seed:        o.Seed + 3,
		Prefix:      "demo-pages",
		Log:         o.Log,
	}); err != nil {
		return fmt.Errorf("page size study: %w", err)
	}
	return nil
}
