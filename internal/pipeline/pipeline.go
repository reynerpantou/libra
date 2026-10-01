// Package pipeline is Libra's data pipeline. Raw exposures and business
// events are appended by the ingestion API; each run rolls new rows up into
// two tables reports read from:
//
//   - assignments: each unit's first exposure to each experiment (variant,
//     time, and the request attributes used for dimension breakdowns).
//   - unit_measure_daily: every measure's value per unit per day, keyed by
//     user id and, for events that name one, by device id too.
//
// Runs are incremental. A watermark on row ids tracks what's been processed;
// for events, each (business, day) that received new rows is recomputed from
// scratch for that day, so late or re-sent events are handled and a failed
// run is safely retried. A measure that is new or whose definition changed is
// backfilled over all days.
package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// lockID is the Postgres advisory lock that keeps runs from overlapping,
// including across several Libra instances.
const lockID = 0x4c49425241 // "LIBRA"

// settle skips rows ingested in the last few seconds: row ids are assigned
// before commit, so the newest ids may still have uncommitted neighbours.
const settle = 5 * time.Second

// Stats describes what a run did.
type Stats struct {
	Exposures        int64   `json:"exposures"`
	Assignments      int64   `json:"assignments"`
	Events           int64   `json:"events"`
	DaysRecomputed   int     `json:"days_recomputed"`
	MeasuresBackfill int     `json:"measures_backfilled"`
	Rows             int64   `json:"rows_written"`
	Seconds          float64 `json:"seconds"`
}

// ErrBusy means another run holds the lock.
var ErrBusy = errors.New("a pipeline run is already in progress")

// Run processes everything ingested since the last run.
func Run(ctx context.Context, db *sql.DB, trigger string) (Stats, error) {
	return RunSettled(ctx, db, trigger, settle)
}

// RunSettled is Run with a custom settle time. Zero is only safe when no
// one else is writing (e.g. right after a bulk load by the same process).
func RunSettled(ctx context.Context, db *sql.DB, trigger string, settleFor time.Duration) (Stats, error) {
	var st Stats
	conn, err := db.Conn(ctx)
	if err != nil {
		return st, err
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&got); err != nil {
		return st, err
	}
	if !got {
		return st, ErrBusy
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)

	var runID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO pipeline_runs (trigger) VALUES ($1) RETURNING id`, trigger).Scan(&runID); err != nil {
		return st, err
	}
	start := time.Now()
	err = run(ctx, db, &st, settleFor)
	st.Seconds = time.Since(start).Seconds()
	status, msg := "succeeded", ""
	if err != nil {
		status, msg = "failed", err.Error()
	}
	statsJSON, _ := json.Marshal(st)
	if _, uerr := db.ExecContext(context.Background(),
		`UPDATE pipeline_runs SET finished_at = now(), status = $2, stats = $3, error = $4 WHERE id = $1`,
		runID, status, statsJSON, msg); uerr != nil {
		log.Printf("pipeline: record run: %v", uerr)
	}
	return st, err
}

func run(ctx context.Context, db *sql.DB, st *Stats, settleFor time.Duration) error {
	var expWM, evWM int64
	if err := db.QueryRowContext(ctx, `SELECT exposure_watermark, event_watermark FROM pipeline_state`).Scan(&expWM, &evWM); err != nil {
		return err
	}
	cutoff := time.Now().Add(-settleFor)
	if err := rollupExposures(ctx, db, expWM, cutoff, st); err != nil {
		return fmt.Errorf("exposures: %w", err)
	}
	if err := rollupEvents(ctx, db, evWM, cutoff, st); err != nil {
		return fmt.Errorf("events: %w", err)
	}
	return nil
}

// rollupExposures folds new exposures into assignments. A unit keeps its
// earliest exposure; seeing it in a second variant marks it multi_variant,
// which reports exclude (it usually means a split changed or a client bug).
func rollupExposures(ctx context.Context, db *sql.DB, wm int64, cutoff time.Time, st *Stats) error {
	var hi sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT max(id) FROM exposures WHERE id > $1 AND ingested_at < $2`, wm, cutoff).Scan(&hi); err != nil {
		return err
	}
	if !hi.Valid {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		WITH batch AS (
			SELECT experiment_id, unit_id, variant_id, ts, attrs, unit_type FROM exposures WHERE id > $1 AND id <= $2
		), variants_seen AS (
			SELECT experiment_id, unit_id, count(DISTINCT variant_id) AS n FROM batch GROUP BY 1, 2
		), firsts AS (
			SELECT DISTINCT ON (experiment_id, unit_id) experiment_id, unit_id, variant_id, ts, attrs, unit_type
			FROM batch ORDER BY experiment_id, unit_id, ts
		)
		INSERT INTO assignments AS a (experiment_id, unit_id, variant_id, first_ts, first_day, multi_variant, dims, unit_type)
		SELECT f.experiment_id, f.unit_id, f.variant_id, f.ts, (f.ts AT TIME ZONE 'UTC')::date, v.n > 1, f.attrs, f.unit_type
		FROM firsts f JOIN variants_seen v USING (experiment_id, unit_id)
		ON CONFLICT (experiment_id, unit_id) DO UPDATE SET
			multi_variant = a.multi_variant OR EXCLUDED.multi_variant OR a.variant_id <> EXCLUDED.variant_id,
			variant_id = CASE WHEN EXCLUDED.first_ts < a.first_ts THEN EXCLUDED.variant_id ELSE a.variant_id END,
			dims       = CASE WHEN EXCLUDED.first_ts < a.first_ts THEN EXCLUDED.dims ELSE a.dims END,
			first_ts   = LEAST(a.first_ts, EXCLUDED.first_ts),
			first_day  = LEAST(a.first_day, EXCLUDED.first_day)`, wm, hi.Int64)
	if err != nil {
		return err
	}
	st.Assignments, _ = res.RowsAffected()
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM exposures WHERE id > $1 AND id <= $2`, wm, hi.Int64).Scan(&st.Exposures); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_state SET exposure_watermark = $1`, hi.Int64); err != nil {
		return err
	}
	return tx.Commit()
}

type storedMeasure struct {
	Measure
	businesses []int64 // whose events count: the business, or all of the platform's
	backfill   bool
	updatedAt  time.Time
}

func rollupEvents(ctx context.Context, db *sql.DB, wm int64, cutoff time.Time, st *Stats) error {
	var hi sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT max(id) FROM events WHERE id > $1 AND ingested_at < $2`, wm, cutoff).Scan(&hi); err != nil {
		return err
	}
	top := wm
	if hi.Valid {
		top = hi.Int64
	}

	// Which (business, day) partitions got new events.
	touched := map[int64][]time.Time{}
	if top > wm {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE id > $1 AND id <= $2`, wm, top).Scan(&st.Events); err != nil {
			return err
		}
		rows, err := db.QueryContext(ctx, `
			SELECT DISTINCT business_id, (ts AT TIME ZONE 'UTC')::date FROM events WHERE id > $1 AND id <= $2`, wm, top)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b int64
			var d time.Time
			if err := rows.Scan(&b, &d); err != nil {
				rows.Close()
				return err
			}
			touched[b] = append(touched[b], d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	measures, err := loadMeasures(ctx, db)
	if err != nil {
		return err
	}
	diversions, err := loadDiversions(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range measures {
		switch {
		case m.backfill:
			n, err := recompute(ctx, db, m, diversions, nil, top)
			if err != nil {
				return fmt.Errorf("backfill %s: %w", m.Key, err)
			}
			st.Rows += n
			st.MeasuresBackfill++
			// Only clear the flag if nobody edited the measure meanwhile.
			if _, err := db.ExecContext(ctx,
				`UPDATE measures SET needs_backfill = false WHERE id = $1 AND updated_at = $2`, m.ID, m.updatedAt); err != nil {
				return err
			}
		case len(daysFor(touched, m.businesses)) > 0:
			n, err := recompute(ctx, db, m, diversions, daysFor(touched, m.businesses), top)
			if err != nil {
				return fmt.Errorf("measure %s: %w", m.Key, err)
			}
			st.Rows += n
		}
	}
	for _, days := range touched {
		st.DaysRecomputed += len(days)
	}
	_, err = db.ExecContext(ctx, `UPDATE pipeline_state SET event_watermark = $1`, top)
	return err
}

func loadMeasures(ctx context.Context, db *sql.DB) ([]storedMeasure, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.business_id, 0), COALESCE(m.platform_id, 0), m.key, m.name, m.event_name, m.aggregation, m.value_field,
		       m.filters, m.needs_backfill, m.updated_at,
		       CASE WHEN m.business_id IS NOT NULL THEN m.business_id::text
		            ELSE COALESCE((SELECT string_agg(b.id::text, ',') FROM businesses b WHERE b.platform_id = m.platform_id), '') END
		FROM measures m ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedMeasure
	for rows.Next() {
		var m storedMeasure
		var filters []byte
		var biz string
		if err := rows.Scan(&m.ID, &m.BusinessID, &m.PlatformID, &m.Key, &m.Name, &m.EventName, &m.Aggregation, &m.ValueField, &filters, &m.backfill, &m.updatedAt, &biz); err != nil {
			return nil, err
		}
		for _, part := range strings.Split(biz, ",") {
			var id int64
			if _, err := fmt.Sscan(part, &id); err == nil {
				m.businesses = append(m.businesses, id)
			}
		}
		if err := json.Unmarshal(filters, &m.Filters); err != nil {
			return nil, fmt.Errorf("measure %s filters: %w", m.Key, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// recompute rebuilds a measure's daily rows for the given days (all days when
// days is nil) from events up to id maxID, atomically.
// daysFor merges the touched days of several businesses.
func daysFor(touched map[int64][]time.Time, businesses []int64) []time.Time {
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, b := range businesses {
		for _, d := range touched[b] {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// loadDiversions lists the ids measures are computed per: user and device
// always, plus any other diversion a layer splits by.
func loadDiversions(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT key FROM diversions WHERE key IN ('user_id', 'device_id') OR key IN (SELECT diversion FROM layers) ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func recompute(ctx context.Context, db *sql.DB, sm storedMeasure, diversions []string, days []time.Time, maxID int64) (int64, error) {
	m := sm.Measure
	if len(sm.businesses) == 0 {
		return 0, nil // a platform with no businesses yet
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	args := &sqlArgs{}
	mid := args.add(m.ID)
	agg, conds := m.compile(args)
	where := []string{
		"business_id = ANY(" + args.add(sm.businesses) + "::bigint[])",
		"event_name = " + args.add(m.EventName),
		"id <= " + args.add(maxID),
	}
	del := `DELETE FROM unit_measure_daily WHERE measure_id = $1`
	delArgs := []any{m.ID}
	if days != nil {
		lo, hi := days[0], days[0]
		for _, d := range days {
			if d.Before(lo) {
				lo = d
			}
			if d.After(hi) {
				hi = d
			}
		}
		dayStrs := make([]string, len(days))
		for i, d := range days {
			dayStrs[i] = d.Format("2006-01-02")
		}
		// The ts range lets the (business, event, ts) index do the work;
		// the day list then keeps untouched days in between out.
		where = append(where,
			"ts >= "+args.add(lo)+"::timestamptz",
			"ts < "+args.add(hi.AddDate(0, 0, 1))+"::timestamptz",
			"(ts AT TIME ZONE 'UTC')::date = ANY("+args.add(dayStrs)+"::date[])")
		del += ` AND day = ANY($2::date[])`
		delArgs = append(delArgs, dayStrs)
	}
	if _, err := tx.ExecContext(ctx, del, delArgs...); err != nil {
		return 0, err
	}
	where = append(where, conds...)
	// One block per diversion: the same events, grouped by that id.
	var parts []string
	for _, d := range diversions {
		var unit string
		switch d {
		case "user_id":
			unit = "NULLIF(unit_id, '')"
		case "device_id":
			unit = "NULLIF(device_id, '')"
		default:
			unit = "(ids->>" + args.add(d) + "::text)"
		}
		parts = append(parts, fmt.Sprintf(`
		SELECT %[1]s::bigint, (ts AT TIME ZONE 'UTC')::date AS day, %[4]s::text, %[5]s, %[2]s
		FROM events WHERE %[3]s AND %[5]s IS NOT NULL
		GROUP BY 2, 4
		HAVING %[2]s IS NOT NULL`, mid, agg, strings.Join(where, " AND "), args.add(d), unit))
	}
	q := `INSERT INTO unit_measure_daily (measure_id, day, unit_type, unit_id, value)` + strings.Join(parts, "\n\t\tUNION ALL")
	res, err := tx.ExecContext(ctx, q, args.list...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// Scheduler runs the pipeline on an interval and on demand.
type Scheduler struct {
	db      *sql.DB
	trigger chan string
}

func NewScheduler(db *sql.DB) *Scheduler {
	return &Scheduler{db: db, trigger: make(chan string, 1)}
}

// Kick asks for a run as soon as possible. It never blocks; a run already
// queued absorbs the request.
func (s *Scheduler) Kick(trigger string) {
	select {
	case s.trigger <- trigger:
	default:
	}
}

// Loop runs until ctx ends. interval <= 0 means on-demand only.
func (s *Scheduler) Loop(ctx context.Context, interval time.Duration) {
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		trigger := "schedule"
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case trigger = <-s.trigger:
		}
		st, err := Run(ctx, s.db, trigger)
		switch {
		case errors.Is(err, ErrBusy):
		case err != nil:
			log.Printf("pipeline (%s): %v", trigger, err)
		case st.Exposures+st.Events > 0 || st.MeasuresBackfill > 0:
			log.Printf("pipeline (%s): %d exposures, %d events, %d measures backfilled, %d rows in %.1fs",
				trigger, st.Exposures, st.Events, st.MeasuresBackfill, st.Rows, st.Seconds)
		}
	}
}

// DataThrough is the time up to which raw data has been processed: the
// start of the last successful run, minus the settle window.
func DataThrough(ctx context.Context, db *sql.DB) (time.Time, bool) {
	var t sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT max(started_at) FROM pipeline_runs WHERE status = 'succeeded'`).Scan(&t); err != nil || !t.Valid {
		return time.Time{}, false
	}
	return t.Time.Add(-settle), true
}
