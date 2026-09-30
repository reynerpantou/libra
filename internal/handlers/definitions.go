package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/formula"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/report"
)

// Measures, metrics and metric groups belong to a business or a platform.
// The same handlers serve both: routes under /businesses/{id}/… and
// /platforms/{pid}/…. A business also sees (read-only) its platform's
// definitions, marked "inherited".

// scopeFromPath reads the scope from the route.
func (s *Server) scopeFromPath(w http.ResponseWriter, r *http.Request) (report.Scope, bool) {
	var sc report.Scope
	var n int
	if r.PathValue("pid") != "" {
		sc.PlatformID, _ = pathID(r, "pid")
		if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM platforms WHERE id = $1`, sc.PlatformID).Scan(&n); n == 0 {
			notFound(w, "platform")
			return sc, false
		}
		return sc, true
	}
	sc.BusinessID, _ = pathID(r, "id")
	if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM businesses WHERE id = $1`, sc.BusinessID).Scan(&n); n == 0 {
		notFound(w, "business")
		return sc, false
	}
	return sc, true
}

// scopeOfRow reads the scope a measure, metric or group belongs to.
func (s *Server) scopeOfRow(ctx context.Context, table string, id int64) (report.Scope, string, error) {
	var sc report.Scope
	var key string
	col := "key"
	if table == "metric_groups" {
		col = "name"
	}
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(business_id, 0), COALESCE(platform_id, 0), `+col+` FROM `+table+` WHERE id = $1`, id).
		Scan(&sc.BusinessID, &sc.PlatformID, &key)
	return sc, key, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// familyDefs loads every definition set that could reference something in
// the scope: the scope itself and, for a platform, each of its businesses.
func (s *Server) familyDefs(ctx context.Context, sc report.Scope) ([]*report.Definitions, error) {
	own, err := report.LoadScope(ctx, s.DB, sc)
	if err != nil {
		return nil, err
	}
	out := []*report.Definitions{own}
	if sc.PlatformID == 0 {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM businesses WHERE platform_id = $1`, sc.PlatformID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		d, err := report.Load(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// usersOf lists the metrics (across the family) whose formulas mention key,
// directly or through other metrics. A measure key is found through
// expansion; a metric key by name.
func usersOf(family []*report.Definitions, key string, isMetric bool) []string {
	var out []string
	seen := map[int64]bool{}
	for _, d := range family {
		for _, id := range d.Order {
			m := d.MetricByID[id]
			if seen[id] || m.Key == key {
				continue
			}
			var names []string
			if isMetric {
				if n, err := formula.Parse(m.Formula); err == nil {
					names = formula.Idents(n)
				}
			} else if n, err := d.Expand(m.Formula, m.Key); err == nil {
				names = report.MeasureKeys(n)
			}
			for _, x := range names {
				if x == key {
					seen[id] = true
					out = append(out, m.Key)
					break
				}
			}
		}
	}
	return out
}

func inherited(sc report.Scope, businessID, platformID int64) bool {
	return sc.BusinessID != 0 && businessID == 0 && platformID != 0
}

// ---- measures ----

type measureOut struct {
	pipeline.Measure
	Pending   bool      `json:"pending_backfill"`
	UpdatedAt time.Time `json:"updated_at"`
	UsedBy    []string  `json:"used_by"`
	Describe  []string  `json:"filters_text"`
	Inherited bool      `json:"inherited"` // defined on the platform
}

func (s *Server) ListMeasures(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	family, err := s.familyDefs(r.Context(), sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defs := family[0]
	pending := map[int64]bool{}
	updated := map[int64]time.Time{}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, needs_backfill, updated_at FROM measures WHERE id = ANY($1::bigint[])`, idsOfMeasures(defs))
	if err != nil {
		serverError(w, r, err)
		return
	}
	for rows.Next() {
		var id int64
		var p bool
		var u time.Time
		_ = rows.Scan(&id, &p, &u)
		pending[id], updated[id] = p, u
	}
	rows.Close()
	out := []measureOut{}
	for _, m := range defs.MeasureByID {
		o := measureOut{Measure: m, Pending: pending[m.ID], UpdatedAt: updated[m.ID], UsedBy: usersOf(family, m.Key, false), Describe: []string{},
			Inherited: inherited(sc, m.BusinessID, m.PlatformID)}
		if o.UsedBy == nil {
			o.UsedBy = []string{}
		}
		for _, f := range m.Filters {
			o.Describe = append(o.Describe, pipeline.DescribeFilter(f))
		}
		out = append(out, o)
	}
	sortByID(out, func(o measureOut) int64 { return o.ID })
	writeJSON(w, http.StatusOK, out)
}

func idsOfMeasures(d *report.Definitions) []int64 {
	out := []int64{}
	for id := range d.MeasureByID {
		out = append(out, id)
	}
	return out
}

type measureRequest struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	EventName   string            `json:"event_name"`
	Aggregation string            `json:"aggregation"`
	ValueField  string            `json:"value_field"`
	Filters     []pipeline.Filter `json:"filters"`
}

func (req measureRequest) measure(sc report.Scope) pipeline.Measure {
	m := pipeline.Measure{
		BusinessID: sc.BusinessID, PlatformID: sc.PlatformID, Key: strings.TrimSpace(req.Key), Name: strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description), EventName: strings.TrimSpace(req.EventName),
		Aggregation: req.Aggregation, ValueField: strings.TrimSpace(req.ValueField), Filters: req.Filters,
	}
	if m.Filters == nil {
		m.Filters = []pipeline.Filter{}
	}
	for i := range m.Filters {
		m.Filters[i].Field = strings.TrimSpace(m.Filters[i].Field)
		for j := range m.Filters[i].Values {
			m.Filters[i].Values[j] = strings.TrimSpace(m.Filters[i].Values[j])
		}
	}
	return m
}

func (s *Server) CreateMeasure(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var req measureRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	m := req.measure(sc)
	if err := m.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	if msg, err := report.KeyTaken(r.Context(), s.DB, sc, m.Key, 0, 0); err != nil {
		serverError(w, r, err)
		return
	} else if msg != "" {
		badRequest(w, msg)
		return
	}
	filters, _ := json.Marshal(m.Filters)
	err := s.DB.QueryRowContext(r.Context(), `
		INSERT INTO measures (business_id, platform_id, key, name, description, event_name, aggregation, value_field, filters)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		nullID(sc.BusinessID), nullID(sc.PlatformID), m.Key, m.Name, m.Description, m.EventName, m.Aggregation, m.ValueField, filters).Scan(&m.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "measure", m.ID, "create", "", "", m)
	s.Pipeline.Kick("measure created")
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) UpdateMeasure(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	sc, oldKey, err := s.scopeOfRow(r.Context(), "measures", id)
	if err != nil {
		notFound(w, "measure")
		return
	}
	var req measureRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	m := req.measure(sc)
	m.ID = id
	if err := m.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	if msg, err := report.KeyTaken(r.Context(), s.DB, sc, m.Key, id, 0); err != nil {
		serverError(w, r, err)
		return
	} else if msg != "" {
		badRequest(w, msg)
		return
	}
	if m.Key != oldKey {
		family, err := s.familyDefs(r.Context(), sc)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if users := usersOf(family, oldKey, false); len(users) > 0 {
			badRequest(w, "can't rename: metrics "+strings.Join(users, ", ")+" use this key")
			return
		}
	}
	filters, _ := json.Marshal(m.Filters)
	// Any definition change invalidates computed values: backfill.
	if _, err := s.DB.ExecContext(r.Context(), `
		UPDATE measures SET key = $2, name = $3, description = $4, event_name = $5, aggregation = $6, value_field = $7,
		       filters = $8, needs_backfill = true, updated_at = now()
		WHERE id = $1`, id, m.Key, m.Name, m.Description, m.EventName, m.Aggregation, m.ValueField, filters); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "measure", id, "update", "", "", m)
	s.Pipeline.Kick("measure updated")
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) DeleteMeasure(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	sc, key, err := s.scopeOfRow(r.Context(), "measures", id)
	if err != nil {
		notFound(w, "measure")
		return
	}
	family, err := s.familyDefs(r.Context(), sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if users := usersOf(family, key, false); len(users) > 0 {
		badRequest(w, "metrics "+strings.Join(users, ", ")+" use this measure; change them first")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM measures WHERE id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "measure", id, "delete", "", "", map[string]string{"key": key})
	w.WriteHeader(http.StatusNoContent)
}

// ---- metrics ----

type metricOut struct {
	report.MetricDef
	Expanded  string       `json:"expanded"`
	Kind      formula.Kind `json:"kind"`
	Measures  []string     `json:"measures"`
	Error     string       `json:"error,omitempty"`
	Inherited bool         `json:"inherited"`
}

func (s *Server) ListMetrics(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	defs, err := report.LoadScope(r.Context(), s.DB, sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := []metricOut{}
	for _, id := range defs.Order {
		m := defs.MetricByID[id]
		o := metricOut{MetricDef: m, Measures: []string{}, Inherited: inherited(sc, m.BusinessID, m.PlatformID)}
		if n, err := defs.Expand(m.Formula, m.Key); err != nil {
			o.Error = err.Error()
		} else {
			o.Expanded, o.Kind, o.Measures = formula.String(n), formula.Classify(n), report.MeasureKeys(n)
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

type metricRequest struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Formula     string `json:"formula"`
	Format      string `json:"format"`
	Decimals    int    `json:"decimals"`
	Direction   string `json:"direction"`
}

func (req metricRequest) metric(sc report.Scope, id int64) report.MetricDef {
	return report.MetricDef{
		ID: id, BusinessID: sc.BusinessID, PlatformID: sc.PlatformID, Key: strings.TrimSpace(req.Key), Name: strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description), Formula: strings.TrimSpace(req.Formula),
		Format: req.Format, Decimals: req.Decimals, Direction: req.Direction,
	}
}

func (s *Server) CreateMetric(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var req metricRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	s.saveMetric(w, r, req.metric(sc, 0))
}

func (s *Server) UpdateMetric(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	sc, _, err := s.scopeOfRow(r.Context(), "metrics", id)
	if err != nil {
		notFound(w, "metric")
		return
	}
	var req metricRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	s.saveMetric(w, r, req.metric(sc, id))
}

func (s *Server) saveMetric(w http.ResponseWriter, r *http.Request, m report.MetricDef) {
	sc := m.ScopeOf()
	if msg, err := report.KeyTaken(r.Context(), s.DB, sc, m.Key, 0, m.ID); err != nil {
		serverError(w, r, err)
		return
	} else if msg != "" {
		badRequest(w, msg)
		return
	}
	defs, err := report.LoadScope(r.Context(), s.DB, sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	expanded, kind, err := defs.ValidateMetric(m)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	action := "update"
	if m.ID == 0 {
		action = "create"
		err = s.DB.QueryRowContext(r.Context(), `
			INSERT INTO metrics (business_id, platform_id, key, name, description, formula, format, decimals, direction)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			nullID(sc.BusinessID), nullID(sc.PlatformID), m.Key, m.Name, m.Description, m.Formula, m.Format, m.Decimals, m.Direction).Scan(&m.ID)
	} else {
		_, err = s.DB.ExecContext(r.Context(), `
			UPDATE metrics SET key = $2, name = $3, description = $4, formula = $5, format = $6, decimals = $7, direction = $8, updated_at = now()
			WHERE id = $1`, m.ID, m.Key, m.Name, m.Description, m.Formula, m.Format, m.Decimals, m.Direction)
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a metric with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "metric", m.ID, action, "", "", m)
	status := http.StatusOK
	if action == "create" {
		status = http.StatusCreated
	}
	writeJSON(w, status, metricOut{MetricDef: m, Expanded: expanded, Kind: kind, Measures: []string{}})
}

func (s *Server) DeleteMetric(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	sc, key, err := s.scopeOfRow(r.Context(), "metrics", id)
	if err != nil {
		notFound(w, "metric")
		return
	}
	family, err := s.familyDefs(r.Context(), sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if users := usersOf(family, key, true); len(users) > 0 {
		badRequest(w, "metrics "+strings.Join(users, ", ")+" use this metric; change them first")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	// Groups anywhere may include it (experiments can mix businesses).
	if _, err := tx.ExecContext(r.Context(), `UPDATE metric_groups SET metric_ids = array_remove(metric_ids, $1::bigint) WHERE $1 = ANY(metric_ids)`, id); err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM metrics WHERE id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "metric", id, "delete", "", "", map[string]string{"key": key})
	w.WriteHeader(http.StatusNoContent)
}

// ValidateFormula checks a formula while it's being typed.
func (s *Server) ValidateFormula(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Formula string `json:"formula"`
		Key     string `json:"key"`
		ID      int64  `json:"id"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	defs, err := report.LoadScope(r.Context(), s.DB, sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	self := req.Key
	if !formula.ValidIdent(self) {
		self = ""
	}
	n, err := defs.Expand(req.Formula, self)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "expanded": formula.String(n), "kind": formula.Classify(n), "measures": report.MeasureKeys(n),
	})
}

// PreviewFormula evaluates a formula over recent data.
func (s *Server) PreviewFormula(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Formula string `json:"formula"`
		Days    int    `json:"days"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	if req.Days <= 0 || req.Days > 90 {
		req.Days = 7
	}
	defs, err := report.LoadScope(r.Context(), s.DB, sc)
	if err != nil {
		serverError(w, r, err)
		return
	}
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -(req.Days - 1))
	out, err := report.Preview(r.Context(), s.DB, defs, req.Formula, from, to)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out["ok"] = true
	writeJSON(w, http.StatusOK, out)
}

// ---- metric groups ----

type MetricGroup struct {
	ID          int64   `json:"id"`
	BusinessID  int64   `json:"business_id,omitempty"`
	PlatformID  int64   `json:"platform_id,omitempty"`
	Owner       string  `json:"owner"`      // business or platform name
	OwnerKind   string  `json:"owner_kind"` // business | platform
	PlatformKey string  `json:"platform"`   // the platform it sits under
	Name        string  `json:"name"`
	Description string  `json:"description"`
	IsDefault   bool    `json:"is_default"` // included in every experiment of its business / platform
	MetricIDs   []int64 `json:"metric_ids"`
	Inherited   bool    `json:"inherited"`
}

const groupSelect = `
	SELECT g.id, COALESCE(g.business_id, 0), COALESCE(g.platform_id, 0), COALESCE(b.name, p.name),
	       CASE WHEN g.business_id IS NULL THEN 'platform' ELSE 'business' END, COALESCE(bp.name, p.name, ''),
	       g.name, g.description, g.is_default, array_to_string(g.metric_ids, ',')
	FROM metric_groups g
	LEFT JOIN businesses b ON b.id = g.business_id
	LEFT JOIN platforms bp ON bp.id = b.platform_id
	LEFT JOIN platforms p ON p.id = g.platform_id`

func (s *Server) queryGroups(ctx context.Context, where string, args ...any) ([]MetricGroup, error) {
	rows, err := s.DB.QueryContext(ctx, groupSelect+` WHERE `+where+` ORDER BY COALESCE(bp.name, p.name), g.platform_id NULLS LAST, COALESCE(b.name, ''), g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricGroup{}
	for rows.Next() {
		var g MetricGroup
		var ids string
		if err := rows.Scan(&g.ID, &g.BusinessID, &g.PlatformID, &g.Owner, &g.OwnerKind, &g.PlatformKey, &g.Name, &g.Description, &g.IsDefault, &ids); err != nil {
			return nil, err
		}
		g.MetricIDs = parseIDs(ids)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListMetricGroups lists a scope's groups; a business also sees its
// platform's (inherited).
func (s *Server) ListMetricGroups(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var gs []MetricGroup
	var err error
	if sc.PlatformID != 0 {
		gs, err = s.queryGroups(r.Context(), `g.platform_id = $1`, sc.PlatformID)
	} else {
		gs, err = s.queryGroups(r.Context(), `g.business_id = $1 OR g.platform_id = (SELECT platform_id FROM businesses WHERE id = $1)`, sc.BusinessID)
		for i := range gs {
			gs[i].Inherited = gs[i].PlatformID != 0
		}
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

// AllMetricGroups lists every group, for picking an experiment's groups
// across businesses and platforms.
func (s *Server) AllMetricGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.queryGroups(r.Context(), `true`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

// AllMetrics lists every metric with its owner, so groups picked from other
// businesses can show their metric names.
func (s *Server) AllMetrics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT m.id, m.key, m.name, m.format, COALESCE(b.name, p.name)
		FROM metrics m LEFT JOIN businesses b ON b.id = m.business_id LEFT JOIN platforms p ON p.id = m.platform_id ORDER BY m.id`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID     int64  `json:"id"`
		Key    string `json:"key"`
		Name   string `json:"name"`
		Format string `json:"format"`
		Owner  string `json:"owner"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Key, &x.Name, &x.Format, &x.Owner); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

type groupRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	IsDefault   bool    `json:"is_default"`
	MetricIDs   []int64 `json:"metric_ids"`
}

func (s *Server) validGroup(ctx context.Context, sc report.Scope, req *groupRequest) string {
	name, ok := trimmed(req.Name, 80)
	if !ok {
		return "name is required"
	}
	req.Name, req.Description = name, strings.TrimSpace(req.Description)
	if req.MetricIDs == nil {
		req.MetricIDs = []int64{}
	}
	defs, err := report.LoadScope(ctx, s.DB, sc)
	if err != nil {
		return "could not check the metrics"
	}
	seen := map[int64]bool{}
	for _, id := range req.MetricIDs {
		if _, ok := defs.MetricByID[id]; !ok || seen[id] {
			if sc.PlatformID != 0 {
				return "a platform group can only hold the platform's own metrics (each once)"
			}
			return "every metric must belong to this business or its platform (each once)"
		}
		seen[id] = true
	}
	return ""
}

func (s *Server) CreateMetricGroup(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromPath(w, r)
	if !ok {
		return
	}
	var req groupRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := s.validGroup(r.Context(), sc, &req); msg != "" {
		badRequest(w, msg)
		return
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(), `
		INSERT INTO metric_groups (business_id, platform_id, name, description, is_default, metric_ids) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		nullID(sc.BusinessID), nullID(sc.PlatformID), req.Name, req.Description, req.IsDefault, req.MetricIDs).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a metric group with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, MetricGroup{ID: id, BusinessID: sc.BusinessID, PlatformID: sc.PlatformID, Name: req.Name,
		Description: req.Description, IsDefault: req.IsDefault, MetricIDs: req.MetricIDs})
}

func (s *Server) UpdateMetricGroup(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	sc, _, err := s.scopeOfRow(r.Context(), "metric_groups", id)
	if err != nil {
		notFound(w, "metric group")
		return
	}
	var req groupRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := s.validGroup(r.Context(), sc, &req); msg != "" {
		badRequest(w, msg)
		return
	}
	_, err = s.DB.ExecContext(r.Context(), `UPDATE metric_groups SET name = $2, description = $3, is_default = $4, metric_ids = $5 WHERE id = $1`,
		id, req.Name, req.Description, req.IsDefault, req.MetricIDs)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a metric group with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, MetricGroup{ID: id, BusinessID: sc.BusinessID, PlatformID: sc.PlatformID, Name: req.Name,
		Description: req.Description, IsDefault: req.IsDefault, MetricIDs: req.MetricIDs})
}

func (s *Server) DeleteMetricGroup(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE experiments SET metric_group_ids = array_remove(metric_group_ids, $1::bigint) WHERE $1 = ANY(metric_group_ids)`, id); err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM metric_groups WHERE id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
