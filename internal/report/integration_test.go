package report_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/reynerpantou/libra/internal/database"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/report"
	"github.com/reynerpantou/libra/internal/serving"
	"github.com/reynerpantou/libra/internal/simulate"
)

// TestEndToEnd seeds the demo, simulates traffic, runs the pipeline and
// checks the report recovers the simulated effects. It needs an empty
// Postgres database: LIBRA_TEST_DATABASE_URL=postgres://.../libra_test
func TestEndToEnd(t *testing.T) {
	dsn := os.Getenv("LIBRA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("LIBRA_TEST_DATABASE_URL not set")
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
	if err := simulate.Seed(ctx, db, 0); err != nil {
		t.Fatalf("seed (is the database empty?): %v", err)
	}
	st, err := simulate.Generate(ctx, db, simulate.Options{Users: 12000, Days: 10, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if st.Events == 0 || st.Exposures == 0 {
		t.Fatalf("nothing generated: %+v", st)
	}
	ps, err := pipeline.RunSettled(ctx, db, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Assignments == 0 || ps.Rows == 0 {
		t.Fatalf("pipeline wrote nothing: %+v", ps)
	}

	var expID, bizID int64
	if err := db.QueryRow(`SELECT id, business_id FROM experiments WHERE name = 'Ranking formula v2'`).Scan(&expID, &bizID); err != nil {
		t.Fatal(err)
	}
	defs, err := report.Load(ctx, db, bizID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := report.Compute(ctx, db, expID, report.Options{MetricIDs: defs.Order})
	if err != nil {
		t.Fatal(err)
	}
	if r.SRM.Suspect {
		t.Errorf("SRM flagged on a correct split: %+v", r.SRM)
	}
	byKey := map[string]report.MetricResult{}
	for _, m := range r.Segments[0].Metrics {
		if m.Error != "" {
			t.Errorf("metric %s: %s", m.Key, m.Error)
		}
		byKey[m.Key] = m
	}
	ctr := byKey["ctr"].Comparisons[0]
	if ctr.Verdict != "better" || float64(ctr.RelDiff) < 0.02 || float64(ctr.RelDiff) > 0.09 {
		t.Errorf("CTR should show the simulated +5%% lift: %+v", ctr)
	}
	if byKey["gmv"].Kind != "total" || byKey["gmv_per_user"].Kind != "ratio" {
		t.Errorf("kinds: gmv=%s gmv_per_user=%s", byKey["gmv"].Kind, byKey["gmv_per_user"].Kind)
	}
	// Per-unit comparison of a total equals the per-user metric.
	if byKey["gmv"].Comparisons[0].RelDiff != byKey["gmv_per_user"].Comparisons[0].RelDiff {
		t.Errorf("total vs per-user lift differ")
	}

	// The ads experiment's layer splits by device: its units are devices, and
	// its metrics come from the per-device measures.
	var adsID int64
	if err := db.QueryRow(`SELECT id FROM experiments WHERE name = 'Ads slot position'`).Scan(&adsID); err != nil {
		t.Fatal(err)
	}
	if adsID < 100000000000000 {
		t.Errorf("experiment ids should be random 15-digit numbers, got %d", adsID)
	}
	var devUnits, userUnits int
	_ = db.QueryRow(`SELECT count(*) FILTER (WHERE unit_type = 'device_id'), count(*) FILTER (WHERE unit_type = 'user_id') FROM assignments WHERE experiment_id = $1`, adsID).Scan(&devUnits, &userUnits)
	if devUnits == 0 || userUnits != 0 {
		t.Errorf("ads experiment units: %d devices, %d users", devUnits, userUnits)
	}
	ra, err := report.Compute(ctx, db, adsID, report.Options{MetricIDs: defs.Order})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ra.Segments[0].Metrics {
		if m.Key == "ads_share" && (m.Comparisons[0].Verdict != "changed" || float64(m.Comparisons[0].RelDiff) < 0.1) {
			t.Errorf("ads share should rise with an earlier ad slot: %+v", m.Comparisons[0])
		}
	}

	// Dimension breakdown returns segments by region.
	rd, err := report.Compute(ctx, db, expID, report.Options{MetricIDs: defs.Order[:1], Dimension: "region"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Segments) < 3 {
		t.Errorf("expected region segments, got %d", len(rd.Segments))
	}

	// The trend's last point equals the full report.
	tr, err := report.Trend(ctx, db, expID, byKey["ctr"].MetricID, report.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if last := tr[len(tr)-1]; last.Comparisons[0].RelDiff != ctr.RelDiff {
		t.Errorf("trend end %v != report %v", last.Comparisons[0].RelDiff, ctr.RelDiff)
	}

	// Late events for an existing day are picked up by the next run.
	var unit string
	if err := db.QueryRow(`SELECT unit_id FROM assignments WHERE experiment_id = $1 LIMIT 1`, expID).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	before := sumMeasure(t, db, "search_gmv", unit)
	if err := serving.WriteEvents(ctx, db, []serving.Event{{
		BusinessID: bizID, Name: "order", UnitID: unit, TS: time.Now().UTC().AddDate(0, 0, -1), Value: 1000,
		Props: []byte(`{"source":"search","is_ads":false}`),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.RunSettled(ctx, db, "test", 0); err != nil {
		t.Fatal(err)
	}
	if after := sumMeasure(t, db, "search_gmv", unit); after-before != 1000 {
		t.Errorf("late event not reflected: before %v after %v", before, after)
	}

	// Editing a measure triggers a backfill with the new definition.
	if _, err := db.Exec(`UPDATE measures SET filters = '[]', needs_backfill = true, updated_at = now() WHERE key = 'search_gmv'`); err != nil {
		t.Fatal(err)
	}
	ps, err = pipeline.RunSettled(ctx, db, "test", 0)
	if err != nil || ps.MeasuresBackfill != 1 {
		t.Fatalf("backfill: %+v %v", ps, err)
	}
}

func sumMeasure(t *testing.T, db *sql.DB, key, unit string) float64 {
	t.Helper()
	var v float64
	if err := db.QueryRow(`
		SELECT COALESCE(sum(d.value), 0) FROM unit_measure_daily d JOIN measures m ON m.id = d.measure_id
		WHERE m.key = $1 AND d.unit_id = $2`, key, unit).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
