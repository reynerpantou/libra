package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/formula"
	"github.com/reynerpantou/libra/internal/pipeline"
	"github.com/reynerpantou/libra/internal/report"
)

type Business struct {
	ID            int64     `json:"id"`
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	RequireReview bool      `json:"require_review"`
	CreatedAt     time.Time `json:"created_at"`
	Measures      int       `json:"measures"`
	Metrics       int       `json:"metrics"`
	Experiments   int       `json:"experiments"`
}

var businessKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func (s *Server) ListBusinesses(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT b.id, b.key, b.name, b.description, b.require_review, b.created_at,
		       (SELECT count(*) FROM measures WHERE business_id = b.id),
		       (SELECT count(*) FROM metrics WHERE business_id = b.id),
		       (SELECT count(*) FROM experiments WHERE business_id = b.id AND status <> 'archived')
		FROM businesses b ORDER BY b.name`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Business{}
	for rows.Next() {
		var b Business
		if err := rows.Scan(&b.ID, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt, &b.Measures, &b.Metrics, &b.Experiments); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadBusiness(r *http.Request, id int64) (Business, error) {
	var b Business
	err := s.DB.QueryRowContext(r.Context(), `SELECT id, key, name, description, require_review, created_at FROM businesses WHERE id = $1`, id).
		Scan(&b.ID, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt)
	return b, err
}

func (s *Server) GetBusiness(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	b, err := s.loadBusiness(r, id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "business")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

type businessRequest struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	RequireReview bool   `json:"require_review"`
}

func (s *Server) CreateBusiness(w http.ResponseWriter, r *http.Request) {
	var req businessRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if !businessKey.MatchString(req.Key) {
		badRequest(w, "key must be lowercase letters, digits and _ (e.g. search)")
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(),
		`INSERT INTO businesses (key, name, description, require_review) VALUES ($1, $2, $3, $4) RETURNING id`,
		req.Key, name, strings.TrimSpace(req.Description), req.RequireReview).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a business with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "business", id, "create", "", "", req)
	b, _ := s.loadBusiness(r, id)
	writeJSON(w, http.StatusCreated, b)
}

// UpdateBusiness changes name, description and review policy. The key is
// permanent: services send events with it.
func (s *Server) UpdateBusiness(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req businessRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE businesses SET name = $2, description = $3, require_review = $4 WHERE id = $1`,
		id, name, strings.TrimSpace(req.Description), req.RequireReview)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "business")
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "business", id, "update", "", "", req)
	b, _ := s.loadBusiness(r, id)
	writeJSON(w, http.StatusOK, b)
}

// ---- measures ----

type measureOut struct {
	pipeline.Measure
	Pending   bool      `json:"pending_backfill"`
	UpdatedAt time.Time `json:"updated_at"`
	UsedBy    []string  `json:"used_by"`
	Describe  []string  `json:"filters_text"`
}

func (s *Server) ListMeasures(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	pending := map[int64]bool{}
	updated := map[int64]time.Time{}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, needs_backfill, updated_at FROM measures WHERE business_id = $1`, bid)
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
	usedBy := measureUsage(defs)
	out := []measureOut{}
	for _, m := range defs.MeasureByID {
		o := measureOut{Measure: m, Pending: pending[m.ID], UpdatedAt: updated[m.ID], UsedBy: usedBy[m.Key], Describe: []string{}}
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

// measureUsage maps measure keys to the metrics whose formulas use them.
func measureUsage(defs *report.Definitions) map[string][]string {
	out := map[string][]string{}
	for _, id := range defs.Order {
		m := defs.MetricByID[id]
		n, err := defs.Expand(m.Formula, m.Key)
		if err != nil {
			continue
		}
		for _, k := range report.MeasureKeys(n) {
			out[k] = append(out[k], m.Key)
		}
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

func (req measureRequest) measure(bid int64) pipeline.Measure {
	m := pipeline.Measure{
		BusinessID: bid, Key: strings.TrimSpace(req.Key), Name: strings.TrimSpace(req.Name),
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
	bid, _ := pathID(r, "id")
	var req measureRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	m := req.measure(bid)
	if err := m.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, clash := defs.Metrics[m.Key]; clash {
		badRequest(w, "a metric is already called "+m.Key)
		return
	}
	filters, _ := json.Marshal(m.Filters)
	err = s.DB.QueryRowContext(r.Context(), `
		INSERT INTO measures (business_id, key, name, description, event_name, aggregation, value_field, filters)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		bid, m.Key, m.Name, m.Description, m.EventName, m.Aggregation, m.ValueField, filters).Scan(&m.ID)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a measure with that key exists")
		return
	}
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
	var bid int64
	var oldKey string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT business_id, key FROM measures WHERE id = $1`, id).Scan(&bid, &oldKey); err != nil {
		notFound(w, "measure")
		return
	}
	var req measureRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	m := req.measure(bid)
	m.ID = id
	if err := m.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, clash := defs.Metrics[m.Key]; clash {
		badRequest(w, "a metric is already called "+m.Key)
		return
	}
	if m.Key != oldKey {
		if users := measureUsage(defs)[oldKey]; len(users) > 0 {
			badRequest(w, "can't rename: metrics "+strings.Join(users, ", ")+" use this key")
			return
		}
	}
	filters, _ := json.Marshal(m.Filters)
	// Any definition change invalidates computed values: backfill.
	_, err = s.DB.ExecContext(r.Context(), `
		UPDATE measures SET key = $2, name = $3, description = $4, event_name = $5, aggregation = $6, value_field = $7,
		       filters = $8, needs_backfill = true, updated_at = now()
		WHERE id = $1`, id, m.Key, m.Name, m.Description, m.EventName, m.Aggregation, m.ValueField, filters)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a measure with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "measure", id, "update", "", "", m)
	s.Pipeline.Kick("measure updated")
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) DeleteMeasure(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var bid int64
	var key string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT business_id, key FROM measures WHERE id = $1`, id).Scan(&bid, &key); err != nil {
		notFound(w, "measure")
		return
	}
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if users := measureUsage(defs)[key]; len(users) > 0 {
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
	Expanded string       `json:"expanded"`
	Kind     formula.Kind `json:"kind"`
	Measures []string     `json:"measures"`
	Error    string       `json:"error,omitempty"`
}

func (s *Server) ListMetrics(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := []metricOut{}
	for _, id := range defs.Order {
		m := defs.MetricByID[id]
		o := metricOut{MetricDef: m, Measures: []string{}}
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

func (req metricRequest) metric(bid, id int64) report.MetricDef {
	return report.MetricDef{
		ID: id, BusinessID: bid, Key: strings.TrimSpace(req.Key), Name: strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description), Formula: strings.TrimSpace(req.Formula),
		Format: req.Format, Decimals: req.Decimals, Direction: req.Direction,
	}
}

func (s *Server) CreateMetric(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	var req metricRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	s.saveMetric(w, r, req.metric(bid, 0))
}

func (s *Server) UpdateMetric(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var bid int64
	if err := s.DB.QueryRowContext(r.Context(), `SELECT business_id FROM metrics WHERE id = $1`, id).Scan(&bid); err != nil {
		notFound(w, "metric")
		return
	}
	var req metricRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	s.saveMetric(w, r, req.metric(bid, id))
}

func (s *Server) saveMetric(w http.ResponseWriter, r *http.Request, m report.MetricDef) {
	defs, err := report.Load(r.Context(), s.DB, m.BusinessID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := s.loadBusiness(r, m.BusinessID); err != nil {
		notFound(w, "business")
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
			INSERT INTO metrics (business_id, key, name, description, formula, format, decimals, direction)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			m.BusinessID, m.Key, m.Name, m.Description, m.Formula, m.Format, m.Decimals, m.Direction).Scan(&m.ID)
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
	var bid int64
	var key string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT business_id, key FROM metrics WHERE id = $1`, id).Scan(&bid, &key); err != nil {
		notFound(w, "metric")
		return
	}
	defs, err := report.Load(r.Context(), s.DB, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	for _, other := range defs.Metrics {
		if other.ID == id {
			continue
		}
		n, err := formula.Parse(other.Formula)
		if err != nil {
			continue
		}
		for _, name := range formula.Idents(n) {
			if name == key {
				badRequest(w, "metric "+other.Key+" uses this metric; change it first")
				return
			}
		}
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE metric_groups SET metric_ids = array_remove(metric_ids, $1::bigint) WHERE business_id = $2`, id, bid); err != nil {
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
	bid, _ := pathID(r, "id")
	var req struct {
		Formula string `json:"formula"`
		Key     string `json:"key"`
		ID      int64  `json:"id"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	defs, err := report.Load(r.Context(), s.DB, bid)
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

// PreviewFormula evaluates a formula over recent data for the business.
func (s *Server) PreviewFormula(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
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
	defs, err := report.Load(r.Context(), s.DB, bid)
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
	BusinessID  int64   `json:"business_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	MetricIDs   []int64 `json:"metric_ids"`
}

func (s *Server) ListMetricGroups(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, business_id, name, description, array_to_string(metric_ids, ',') FROM metric_groups WHERE business_id = $1 ORDER BY id`, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []MetricGroup{}
	for rows.Next() {
		var g MetricGroup
		var ids string
		if err := rows.Scan(&g.ID, &g.BusinessID, &g.Name, &g.Description, &ids); err != nil {
			serverError(w, r, err)
			return
		}
		g.MetricIDs = parseIDs(ids)
		out = append(out, g)
	}
	writeJSON(w, http.StatusOK, out)
}

type groupRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	MetricIDs   []int64 `json:"metric_ids"`
}

func (s *Server) validGroup(r *http.Request, bid int64, req *groupRequest) string {
	name, ok := trimmed(req.Name, 80)
	if !ok {
		return "name is required"
	}
	req.Name, req.Description = name, strings.TrimSpace(req.Description)
	if req.MetricIDs == nil {
		req.MetricIDs = []int64{}
	}
	var n int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM metrics WHERE business_id = $1 AND id = ANY($2::bigint[])`, bid, req.MetricIDs).Scan(&n); err != nil || n != len(req.MetricIDs) {
		return "every metric must belong to this business (and appear once)"
	}
	return ""
}

func (s *Server) CreateMetricGroup(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	var req groupRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := s.validGroup(r, bid, &req); msg != "" {
		badRequest(w, msg)
		return
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO metric_groups (business_id, name, description, metric_ids) VALUES ($1, $2, $3, $4) RETURNING id`,
		bid, req.Name, req.Description, req.MetricIDs).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a metric group with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, MetricGroup{ID: id, BusinessID: bid, Name: req.Name, Description: req.Description, MetricIDs: req.MetricIDs})
}

func (s *Server) UpdateMetricGroup(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var bid int64
	if err := s.DB.QueryRowContext(r.Context(), `SELECT business_id FROM metric_groups WHERE id = $1`, id).Scan(&bid); err != nil {
		notFound(w, "metric group")
		return
	}
	var req groupRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := s.validGroup(r, bid, &req); msg != "" {
		badRequest(w, msg)
		return
	}
	_, err := s.DB.ExecContext(r.Context(), `UPDATE metric_groups SET name = $2, description = $3, metric_ids = $4 WHERE id = $1`,
		id, req.Name, req.Description, req.MetricIDs)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a metric group with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, MetricGroup{ID: id, BusinessID: bid, Name: req.Name, Description: req.Description, MetricIDs: req.MetricIDs})
}

func (s *Server) DeleteMetricGroup(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM metric_groups WHERE id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- event exploration ----

// EventSummary shows what a business has been sending: counts per event per
// day and the property names seen, which is what you need to write measures.
func (s *Server) EventSummary(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	since := time.Now().UTC().AddDate(0, 0, -13).Truncate(24 * time.Hour)
	type dayCount struct {
		Day   string  `json:"day"`
		Count int64   `json:"count"`
		Value float64 `json:"value"`
	}
	type eventInfo struct {
		Name  string     `json:"name"`
		Total int64      `json:"total"`
		Units int64      `json:"units"`
		Days  []dayCount `json:"days"`
		Props []string   `json:"props"`
	}
	byName := map[string]*eventInfo{}
	var names []string
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT event_name, (ts AT TIME ZONE 'UTC')::date, count(*), COALESCE(sum(value), 0)
		FROM events WHERE business_id = $1 AND ts >= $2 GROUP BY 1, 2 ORDER BY 1, 2`, bid, since)
	if err != nil {
		serverError(w, r, err)
		return
	}
	for rows.Next() {
		var name string
		var d time.Time
		var c dayCount
		if err := rows.Scan(&name, &d, &c.Count, &c.Value); err != nil {
			rows.Close()
			serverError(w, r, err)
			return
		}
		c.Day = d.Format("2006-01-02")
		e := byName[name]
		if e == nil {
			e = &eventInfo{Name: name, Days: []dayCount{}, Props: []string{}}
			byName[name] = e
			names = append(names, name)
		}
		e.Days = append(e.Days, c)
		e.Total += c.Count
	}
	rows.Close()
	for _, name := range names {
		e := byName[name]
		_ = s.DB.QueryRowContext(r.Context(), `SELECT count(DISTINCT unit_id) FROM events WHERE business_id = $1 AND event_name = $2 AND ts >= $3`, bid, name, since).Scan(&e.Units)
		prows, err := s.DB.QueryContext(r.Context(), `
			SELECT DISTINCT jsonb_object_keys(props) FROM (
				SELECT props FROM events WHERE business_id = $1 AND event_name = $2 ORDER BY id DESC LIMIT 500
			) s ORDER BY 1 LIMIT 50`, bid, name)
		if err == nil {
			for prows.Next() {
				var p string
				if prows.Scan(&p) == nil {
					e.Props = append(e.Props, p)
				}
			}
			prows.Close()
		}
	}
	out := []*eventInfo{}
	for _, name := range names {
		out = append(out, byName[name])
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since.Format("2006-01-02"), "events": out})
}

// RecentEvents shows the latest raw events (for checking an integration).
func (s *Server) RecentEvents(w http.ResponseWriter, r *http.Request) {
	bid, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, event_name, unit_id, ts, value, props, ingested_at FROM events WHERE business_id = $1 ORDER BY id DESC LIMIT 50`, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type ev struct {
		ID         int64           `json:"id"`
		Event      string          `json:"event"`
		UnitID     string          `json:"unit_id"`
		TS         time.Time       `json:"ts"`
		Value      float64         `json:"value"`
		Props      json.RawMessage `json:"props"`
		IngestedAt time.Time       `json:"ingested_at"`
	}
	out := []ev{}
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.ID, &e.Event, &e.UnitID, &e.TS, &e.Value, &e.Props, &e.IngestedAt); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

func sortByID[T any](xs []T, id func(T) int64) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && id(xs[j]) < id(xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// parseIDs reads array_to_string(ids, ',') output.
func parseIDs(s string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}
