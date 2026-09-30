// Package serving keeps an in-memory assignment snapshot fresh and writes
// exposures and events to Postgres in batches. The request path never
// touches the database: Resolve reads an atomically swapped snapshot and
// hands exposures to a buffered background writer.
package serving

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
)

// Store holds the current snapshot.
type Store struct {
	db   *sql.DB
	snap atomic.Pointer[assign.Snapshot]
	mu   sync.Mutex // serializes reloads
}

func NewStore(db *sql.DB) *Store {
	s := &Store{db: db}
	s.snap.Store(assign.NewSnapshot(0, nil, nil))
	return s
}

// Snapshot returns the current snapshot (never nil).
func (s *Store) Snapshot() *assign.Snapshot { return s.snap.Load() }

// Bump records a configuration change so every instance reloads.
func Bump(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := tx.ExecContext(ctx, `UPDATE config_version SET version = version + 1`)
	return err
}

// Reload rebuilds the snapshot if the stored config version moved (or force).
func (s *Store) Reload(ctx context.Context, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM config_version`).Scan(&version); err != nil {
		return err
	}
	if !force && version == s.Snapshot().Version {
		return nil
	}
	snap, err := Load(ctx, s.db, version)
	if err != nil {
		return err
	}
	s.snap.Store(snap)
	return nil
}

// Watch polls for config changes until ctx ends.
func (s *Store) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reload(ctx, false); err != nil {
				log.Printf("serving: reload: %v", err)
			}
		}
	}
}

// Load reads every experiment that can affect serving into a snapshot.
func Load(ctx context.Context, db *sql.DB, version int64) (*assign.Snapshot, error) {
	var layers []*assign.Layer
	rows, err := db.QueryContext(ctx, `SELECT id, name, salt, diversion FROM layers`) // auto layers included
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		l := &assign.Layer{}
		if err := rows.Scan(&l.ID, &l.Name, &l.Salt, &l.Diversion); err != nil {
			rows.Close()
			return nil, err
		}
		layers = append(layers, l)
	}
	rows.Close()

	exps := map[int64]*assign.Experiment{}
	var list []*assign.Experiment
	rows, err = db.QueryContext(ctx, `
		SELECT e.id, b.key, e.layer_id, e.name, e.status, e.salt, array_to_string(e.buckets, ','), e.targeting,
		       COALESCE(e.launched_variant_id, 0), COALESCE(extract(epoch FROM e.launched_at)::bigint, 0),
		       COALESCE(extract(epoch FROM e.started_at)::bigint, 0)
		FROM experiments e JOIN businesses b ON b.id = e.business_id
		WHERE e.status IN ('draft', 'in_review', 'approved', 'rejected', 'active', 'paused', 'launched')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := &assign.Experiment{Whitelist: map[string]int64{}}
		var buckets string
		var targeting []byte
		if err := rows.Scan(&e.ID, &e.BusinessKey, &e.LayerID, &e.Name, &e.Status, &e.Salt, &buckets, &targeting, &e.LaunchedVar, &e.LaunchOrder, &e.StartOrder); err != nil {
			rows.Close()
			return nil, err
		}
		if buckets != "" {
			for _, b := range strings.Split(buckets, ",") {
				var n int
				fmt.Sscan(b, &n)
				e.Buckets = append(e.Buckets, n)
			}
		}
		if err := json.Unmarshal(targeting, &e.Targeting); err != nil {
			rows.Close()
			return nil, fmt.Errorf("experiment %d targeting: %w", e.ID, err)
		}
		exps[e.ID] = e
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.QueryContext(ctx, `
		SELECT v.experiment_id, v.id, v.key, v.name, v.is_control, v.weight, v.params
		FROM variants v JOIN experiments e ON e.id = v.experiment_id
		WHERE e.status IN ('draft', 'in_review', 'approved', 'rejected', 'active', 'paused', 'launched')
		ORDER BY v.experiment_id, v.position, v.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var eid int64
		var v assign.Variant
		var params []byte
		if err := rows.Scan(&eid, &v.ID, &v.Key, &v.Name, &v.IsControl, &v.Weight, &params); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(params, &v.Params); err != nil {
			rows.Close()
			return nil, fmt.Errorf("variant %d params: %w", v.ID, err)
		}
		if e := exps[eid]; e != nil {
			e.Variants = append(e.Variants, v)
		}
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT experiment_id, unit_id, variant_id FROM whitelist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eid, vid int64
		var unit string
		if err := rows.Scan(&eid, &unit, &vid); err != nil {
			return nil, err
		}
		if e := exps[eid]; e != nil {
			e.Whitelist[unit] = vid
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assign.NewSnapshot(version, layers, list), nil
}

// ---- exposure logging ----

// Exposure is one unit seeing one variant.
type Exposure struct {
	ExperimentID int64
	VariantID    int64
	UnitID       string
	UnitType     string // user_id | device_id
	TS           time.Time
	Attrs        []byte // JSON object
}

// Logger batches exposures into Postgres. When the buffer is full, new
// exposures are dropped (and counted) rather than slowing requests down.
type Logger struct {
	db      *sql.DB
	ch      chan Exposure
	Dropped atomic.Int64
	done    chan struct{}

	// recent suppresses repeat exposures of the same unit to the same
	// experiment for a while; the pipeline keeps only the first anyway.
	mu     sync.Mutex
	recent map[string]time.Time
}

const (
	loggerBuffer  = 100_000
	loggerBatch   = 2_000
	loggerFlush   = time.Second
	dedupeFor     = 10 * time.Minute
	dedupeMaxKeys = 500_000
)

func NewLogger(db *sql.DB) *Logger {
	return &Logger{db: db, ch: make(chan Exposure, loggerBuffer), done: make(chan struct{}), recent: map[string]time.Time{}}
}

// Log queues an exposure unless the same one was logged recently.
func (l *Logger) Log(e Exposure) {
	key := fmt.Sprintf("%d:%d:%s:%s", e.ExperimentID, e.VariantID, e.UnitType, e.UnitID)
	l.mu.Lock()
	if t, ok := l.recent[key]; ok && e.TS.Sub(t) < dedupeFor {
		l.mu.Unlock()
		return
	}
	if len(l.recent) > dedupeMaxKeys {
		l.recent = map[string]time.Time{}
	}
	l.recent[key] = e.TS
	l.mu.Unlock()
	select {
	case l.ch <- e:
	default:
		l.Dropped.Add(1)
	}
}

// Run writes batches until ctx ends, then flushes what's left.
func (l *Logger) Run(ctx context.Context) {
	defer close(l.done)
	t := time.NewTicker(loggerFlush)
	defer t.Stop()
	batch := make([]Exposure, 0, loggerBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := WriteExposures(context.Background(), l.db, batch); err != nil {
			log.Printf("exposure logger: dropped %d exposures: %v", len(batch), err)
			l.Dropped.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case e := <-l.ch:
			batch = append(batch, e)
			if len(batch) >= loggerBatch {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case e := <-l.ch:
					batch = append(batch, e)
					if len(batch) >= loggerBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// Wait blocks until Run has flushed and exited.
func (l *Logger) Wait() { <-l.done }

// WriteExposures inserts exposures in one statement.
func WriteExposures(ctx context.Context, db *sql.DB, xs []Exposure) error {
	if len(xs) == 0 {
		return nil
	}
	exp := make([]int64, len(xs))
	vars := make([]int64, len(xs))
	units := make([]string, len(xs))
	ts := make([]time.Time, len(xs))
	attrs := make([]string, len(xs))
	types := make([]string, len(xs))
	for i, x := range xs {
		exp[i], vars[i], units[i], ts[i] = x.ExperimentID, x.VariantID, x.UnitID, x.TS
		types[i] = x.UnitType
		if types[i] == "" {
			types[i] = "user_id"
		}
		attrs[i] = "{}"
		if len(x.Attrs) > 0 {
			attrs[i] = string(x.Attrs)
		}
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO exposures (experiment_id, variant_id, unit_id, ts, attrs, unit_type)
		SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::text[], $4::timestamptz[], $5::jsonb[], $6::text[])`,
		exp, vars, units, ts, attrs, types)
	return err
}

// Event is one business event (an impression, a click, an order...). It
// names the user (UnitID), the device, or both.
type Event struct {
	BusinessID int64
	Name       string
	UnitID     string // user id; may be empty when DeviceID is set
	DeviceID   string
	IDs        []byte // JSON object of other diversion ids, e.g. {"shop_id":"s-1"}
	TS         time.Time
	Value      float64
	Props      []byte // JSON object
}

// WriteEvents inserts events in one statement.
func WriteEvents(ctx context.Context, db *sql.DB, xs []Event) error {
	if len(xs) == 0 {
		return nil
	}
	biz := make([]int64, len(xs))
	names := make([]string, len(xs))
	units := make([]string, len(xs))
	ts := make([]time.Time, len(xs))
	vals := make([]float64, len(xs))
	props := make([]string, len(xs))
	devices := make([]*string, len(xs))
	ids := make([]string, len(xs))
	for i, x := range xs {
		ids[i] = "{}"
		if len(x.IDs) > 0 {
			ids[i] = string(x.IDs)
		}
		if x.DeviceID != "" {
			d := x.DeviceID
			devices[i] = &d
		}
		biz[i], names[i], units[i], ts[i], vals[i] = x.BusinessID, x.Name, x.UnitID, x.TS, x.Value
		props[i] = "{}"
		if len(x.Props) > 0 {
			props[i] = string(x.Props)
		}
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO events (business_id, event_name, unit_id, ts, value, props, device_id, ids)
		SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::timestamptz[], $5::float8[], $6::jsonb[], $7::text[], $8::jsonb[])`,
		biz, names, units, ts, vals, props, devices, ids)
	return err
}

// DimensionAttrs keeps the request attributes worth breaking reports down
// by: a bounded number of short scalar values.
func DimensionAttrs(attrs map[string]any) []byte {
	if len(attrs) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range attrs {
		if len(out) >= 10 || len(k) > 40 {
			continue
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case bool, float64:
			s = fmt.Sprint(x)
		default:
			continue
		}
		if len(s) <= 64 {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(out)
	return b
}
