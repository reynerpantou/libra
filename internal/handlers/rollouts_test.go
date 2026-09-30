package handlers

import (
	"context"
	"os"
	"testing"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/database"
)

// TestRollouts needs an empty Postgres database:
// LIBRA_TEST_ROLLOUT_DATABASE_URL=postgres://.../libra_rollout_test
func TestRollouts(t *testing.T) {
	dsn := os.Getenv("LIBRA_TEST_ROLLOUT_DATABASE_URL")
	if dsn == "" {
		t.Skip("LIBRA_TEST_ROLLOUT_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var pid, bid, lid int64
	tx, err := db.Begin()
	must(err)
	must(tx.QueryRow(`INSERT INTO platforms (key, name) VALUES ('p', 'P') RETURNING id`).Scan(&pid))
	must(createDefaultGroup(ctx, tx, pid))
	must(tx.Commit())
	must(db.QueryRow(`INSERT INTO businesses (platform_id, key, name) VALUES ($1, 'b', 'B') RETURNING id`, pid).Scan(&bid))
	must(db.QueryRow(`INSERT INTO layers (name, salt, diversion) VALUES ('l', 'l', 'user_id') RETURNING id`).Scan(&lid))
	exp := func(name, status string, traffic int) int64 {
		var id int64
		buckets, err := assign.Allocate(nil, traffic, map[int]bool{}, name)
		must(err)
		if buckets == nil || (status != assign.StatusActive && status != assign.StatusPaused) {
			buckets = []int{}
		}
		must(db.QueryRow(`INSERT INTO experiments (business_id, layer_id, name, status, salt, traffic_target, buckets, targeting)
			VALUES ($1, $2, $3, $4, $3, $5, $6, '{"groups":[]}') RETURNING id`, bid, lid, name, status, traffic, buckets).Scan(&id))
		return id
	}
	// alloc gives an experiment n buckets around the layer's other holders.
	alloc := func(e int64, n int) {
		t.Helper()
		tx, err := db.Begin()
		must(err)
		defer tx.Rollback()
		taken, err := takenBuckets(ctx, tx, lid, e)
		must(err)
		cur, salt, err := currentBuckets(ctx, tx, e)
		must(err)
		next, err := assign.Allocate(cur, n, taken, salt)
		must(err)
		_, err = tx.Exec(`UPDATE experiments SET traffic_target = $2, buckets = $3 WHERE id = $1`, e, n, next)
		must(err)
		must(tx.Commit())
	}
	step := func() int {
		t.Helper()
		if _, err := db.Exec(`UPDATE rollouts SET next_at = now() - interval '1 second' WHERE status = 'active'`); err != nil {
			t.Fatal(err)
		}
		n, err := s.RunRollouts(ctx)
		must(err)
		return n
	}
	// plan starts a plan the way the API does (its first step is applied
	// separately by the caller; here the runner applies every step).
	plan := func(e int64, kind string, target, stepBy int) {
		tx, err := db.Begin()
		must(err)
		must(startPlan(ctx, tx, e, kind, 0, 0, target, &Gradual{Step: stepBy, IntervalMinutes: 1}, 0))
		must(tx.Commit())
	}
	state := func(e int64) (traffic, held, rollout int, planStatus string) {
		must(db.QueryRow(`SELECT traffic_target, cardinality(buckets), launch_rollout FROM experiments WHERE id = $1`, e).Scan(&traffic, &held, &rollout))
		_ = db.QueryRow(`SELECT status FROM rollouts WHERE experiment_id = $1 ORDER BY id DESC LIMIT 1`, e).Scan(&planStatus)
		return
	}

	// The platform starts with its built-in default group.
	var builtin, isDefault bool
	must(db.QueryRow(`SELECT builtin, is_default FROM metric_groups WHERE platform_id = $1`, pid).Scan(&builtin, &isDefault))
	if !builtin || !isDefault {
		t.Errorf("default group: builtin=%v default=%v", builtin, isDefault)
	}

	// Traffic ramps 10% → 30% in two steps, keeping the buckets it had.
	a := exp("a", assign.StatusActive, 100)
	plan(a, "traffic", 300, 100)
	step()
	if tr, held, _, st := state(a); tr != 200 || held != 200 || st != "active" {
		t.Fatalf("after one step: traffic %d held %d plan %s", tr, held, st)
	}
	step()
	if tr, _, _, st := state(a); tr != 300 || st != "done" {
		t.Fatalf("after two steps: traffic %d plan %s", tr, st)
	}

	// A full layer blocks a ramp at what's free.
	b := exp("b", assign.StatusActive, 0)
	alloc(b, 500)
	plan(a, "traffic", 800, 300)
	step()
	if tr, _, _, st := state(a); tr != 500 || st != "active" {
		t.Fatalf("capped step: traffic %d plan %s", tr, st)
	}
	step()
	if tr, _, _, st := state(a); tr != 500 || st != "blocked" {
		t.Fatalf("full layer: traffic %d plan %s", tr, st)
	}

	// A paused experiment waits; a stopped one cancels its plan.
	_, err = db.Exec(`UPDATE experiments SET status = 'paused' WHERE id = $1`, b)
	must(err)
	alloc(b, 100)
	plan(b, "traffic", 300, 100)
	step()
	if tr, _, _, st := state(b); tr != 100 || st != "active" {
		t.Fatalf("paused: traffic %d plan %s", tr, st)
	}
	_, err = db.Exec(`UPDATE experiments SET status = 'stopped' WHERE id = $1`, b)
	must(err)
	step()
	if _, _, _, st := state(b); st != "cancelled" {
		t.Fatalf("stopped: plan %s", st)
	}

	// Renaming a platform key moves parameters to the new namespace.
	_, err = db.Exec(`INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position) VALUES ($1, 'v', 'V', true, 1000, '{"p": {"a": 1}}', 0)`, a)
	must(err)
	tx2, err := db.Begin()
	must(err)
	must(rewrapParams(ctx, tx2, `b.platform_id = $3`, "p", "shop", pid))
	must(tx2.Commit())
	var params string
	must(db.QueryRow(`SELECT params::text FROM variants WHERE experiment_id = $1`, a).Scan(&params))
	if params != `{"shop": {"a": 1}}` {
		t.Errorf("rewrapped params: %s", params)
	}

	// A launch rolls out 10% → 60% → 100%.
	c := exp("c", assign.StatusLaunched, 0)
	_, err = db.Exec(`UPDATE experiments SET launch_rollout = 100, launched_at = now() WHERE id = $1`, c)
	must(err)
	plan(c, "launch", 1000, 500)
	step()
	if _, _, ro, st := state(c); ro != 600 || st != "active" {
		t.Fatalf("launch step: rollout %d plan %s", ro, st)
	}
	step()
	if _, _, ro, st := state(c); ro != 1000 || st != "done" {
		t.Fatalf("launch done: rollout %d plan %s", ro, st)
	}
}
