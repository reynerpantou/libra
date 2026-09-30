package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/reynerpantou/libra/internal/serving"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Business struct {
	ID            int64     `json:"id"`
	PlatformID    int64     `json:"platform_id"`
	PlatformName  string    `json:"platform_name"`
	PlatformKey   string    `json:"platform_key"` // variant params are namespaced by it
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
		SELECT b.id, b.platform_id, p.name, p.key, b.key, b.name, b.description, b.require_review, b.created_at,
		       (SELECT count(*) FROM measures WHERE business_id = b.id),
		       (SELECT count(*) FROM metrics WHERE business_id = b.id),
		       (SELECT count(*) FROM experiments WHERE business_id = b.id AND status <> 'archived')
		FROM businesses b JOIN platforms p ON p.id = b.platform_id ORDER BY p.name, b.name`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Business{}
	for rows.Next() {
		var b Business
		if err := rows.Scan(&b.ID, &b.PlatformID, &b.PlatformName, &b.PlatformKey, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt, &b.Measures, &b.Metrics, &b.Experiments); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadBusiness(r *http.Request, id int64) (Business, error) {
	var b Business
	err := s.DB.QueryRowContext(r.Context(), `
		SELECT b.id, b.platform_id, p.name, p.key, b.key, b.name, b.description, b.require_review, b.created_at
		FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE b.id = $1`, id).
		Scan(&b.ID, &b.PlatformID, &b.PlatformName, &b.PlatformKey, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt)
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
	PlatformID    int64  `json:"platform_id"`
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
	var n int
	if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM platforms WHERE id = $1`, req.PlatformID).Scan(&n); n == 0 {
		badRequest(w, "choose the platform this business belongs to")
		return
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(),
		`INSERT INTO businesses (platform_id, key, name, description, require_review) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		req.PlatformID, req.Key, name, strings.TrimSpace(req.Description), req.RequireReview).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "this platform already has a business with that key")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "business", id, "create", "", "", req)
	// Services resolve by platform and business: refresh the snapshot.
	_ = serving.Bump(r.Context(), s.DB)
	s.reload(r.Context())
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
	// Moving to another platform is allowed: its definitions come along,
	// and it picks up the new platform's shared metrics and defaults.
	if req.PlatformID != 0 {
		var n int
		if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM platforms WHERE id = $1`, req.PlatformID).Scan(&n); n == 0 {
			badRequest(w, "unknown platform")
			return
		}
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	var oldKey string
	if err := tx.QueryRowContext(r.Context(), `SELECT p.key FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE b.id = $1`, id).Scan(&oldKey); err != nil {
		notFound(w, "business")
		return
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE businesses SET name = $2, description = $3, require_review = $4, platform_id = COALESCE(NULLIF($5, 0), platform_id) WHERE id = $1`,
		id, name, strings.TrimSpace(req.Description), req.RequireReview, req.PlatformID)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "that platform already has a business with this key")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// Moved: its experiments' parameters move under the new platform key.
	var newKey string
	if err := tx.QueryRowContext(r.Context(), `SELECT p.key FROM businesses b JOIN platforms p ON p.id = b.platform_id WHERE b.id = $1`, id).Scan(&newKey); err != nil {
		serverError(w, r, err)
		return
	}
	if newKey != oldKey {
		if err := rewrapParams(r.Context(), tx, `b.id = $3`, oldKey, newKey, id); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "business", id, "update", "", "", req)
	// Services resolve by platform and business: refresh the snapshot.
	_ = serving.Bump(r.Context(), s.DB)
	s.reload(r.Context())
	b, _ := s.loadBusiness(r, id)
	writeJSON(w, http.StatusOK, b)
}

// DeleteCheck says whether a business can be deleted, and what deleting it
// would remove, so the UI can explain before anyone clicks delete.
func (s *Server) DeleteCheck(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var out struct {
		Experiments         int `json:"experiments"`           // any status, archived included: these block deleting
		ActiveExperiments   int `json:"active_experiments"`    // running or paused
		Measures            int `json:"measures"`              // deleted with it
		Metrics             int `json:"metrics"`               // deleted with it
		Groups              int `json:"groups"`                // deleted with it
		Events              int `json:"events"`                // deleted with it
		OtherExperimentsUse int `json:"other_experiments_use"` // experiments of other businesses reporting its groups
	}
	err := s.DB.QueryRowContext(r.Context(), `
		SELECT (SELECT count(*) FROM experiments WHERE business_id = $1),
		       (SELECT count(*) FROM experiments WHERE business_id = $1 AND status IN ('active', 'paused')),
		       (SELECT count(*) FROM measures WHERE business_id = $1),
		       (SELECT count(*) FROM metrics WHERE business_id = $1),
		       (SELECT count(*) FROM metric_groups WHERE business_id = $1),
		       (SELECT count(*) FROM events WHERE business_id = $1),
		       (SELECT count(*) FROM experiments e WHERE e.business_id <> $1
		          AND e.metric_group_ids && ARRAY(SELECT id FROM metric_groups WHERE business_id = $1))`, id).
		Scan(&out.Experiments, &out.ActiveExperiments, &out.Measures, &out.Metrics, &out.Groups, &out.Events, &out.OtherExperimentsUse)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteBusiness removes a business with no experiments, along with its
// measures, metrics, groups and events.
func (s *Server) DeleteBusiness(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var n int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM experiments WHERE business_id = $1`, id).Scan(&n); err != nil {
		serverError(w, r, err)
		return
	}
	if n > 0 {
		badRequest(w, fmt.Sprintf("the business has %d experiment(s), archived ones included; a business with experiments can't be deleted because their reports and history need it", n))
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	// Events aren't tied by a foreign key (they're append-only and large).
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM events WHERE business_id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `DELETE FROM businesses WHERE id = $1`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "business")
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "business", id, "delete", "", "", nil)
	// Services resolve by platform and business: refresh the snapshot.
	_ = serving.Bump(r.Context(), s.DB)
	s.reload(r.Context())
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
		SELECT id, event_name, unit_id, COALESCE(device_id, ''), ts, value, props, ingested_at FROM events WHERE business_id = $1 ORDER BY id DESC LIMIT 50`, bid)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type ev struct {
		ID         int64           `json:"id"`
		Event      string          `json:"event"`
		UnitID     string          `json:"unit_id"`
		DeviceID   string          `json:"device_id"`
		TS         time.Time       `json:"ts"`
		Value      float64         `json:"value"`
		Props      json.RawMessage `json:"props"`
		IngestedAt time.Time       `json:"ingested_at"`
	}
	out := []ev{}
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.ID, &e.Event, &e.UnitID, &e.DeviceID, &e.TS, &e.Value, &e.Props, &e.IngestedAt); err != nil {
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
