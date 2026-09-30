package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/reynerpantou/libra/internal/serving"
	"net/http"
	"strings"
	"time"
)

// A platform groups businesses (e.g. TikTok Shop → Search, Feed, Checkout)
// and can hold definitions shared by all of them.

type Platform struct {
	ID          int64      `json:"id"`
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	Measures    int        `json:"measures"`
	Metrics     int        `json:"metrics"`
	Groups      int        `json:"metric_groups"`
	Businesses  []Business `json:"businesses"`
}

// loadPlatforms reads platforms (all, or the one with id) with their
// businesses and counts.
func (s *Server) loadPlatforms(ctx context.Context, id int64) ([]*Platform, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.id, p.key, p.name, p.description, p.created_at,
		       (SELECT count(*) FROM measures WHERE platform_id = p.id),
		       (SELECT count(*) FROM metrics WHERE platform_id = p.id),
		       (SELECT count(*) FROM metric_groups WHERE platform_id = p.id)
		FROM platforms p WHERE $1 = 0 OR p.id = $1 ORDER BY p.name`, id)
	if err != nil {
		return nil, err
	}
	out := []*Platform{}
	by := map[int64]*Platform{}
	for rows.Next() {
		p := &Platform{Businesses: []Business{}}
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.Measures, &p.Metrics, &p.Groups); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
		by[p.ID] = p
	}
	rows.Close()
	brows, err := s.DB.QueryContext(ctx, `
		SELECT b.id, b.platform_id, b.key, b.name, b.description, b.require_review, b.created_at,
		       (SELECT count(*) FROM measures WHERE business_id = b.id),
		       (SELECT count(*) FROM metrics WHERE business_id = b.id),
		       (SELECT count(*) FROM experiments WHERE business_id = b.id AND status <> 'archived')
		FROM businesses b WHERE $1 = 0 OR b.platform_id = $1 ORDER BY b.name`, id)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var b Business
		if err := brows.Scan(&b.ID, &b.PlatformID, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt, &b.Measures, &b.Metrics, &b.Experiments); err != nil {
			return nil, err
		}
		if p := by[b.PlatformID]; p != nil {
			b.PlatformName = p.Name
			p.Businesses = append(p.Businesses, b)
		}
	}
	return out, brows.Err()
}

func (s *Server) ListPlatforms(w http.ResponseWriter, r *http.Request) {
	out, err := s.loadPlatforms(r.Context(), 0)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetPlatform(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "pid")
	out, err := s.loadPlatforms(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if id == 0 || len(out) == 0 {
		notFound(w, "platform")
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

type platformRequest struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) CreatePlatform(w http.ResponseWriter, r *http.Request) {
	var req platformRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if !businessKey.MatchString(req.Key) {
		badRequest(w, "key must be lowercase letters, digits and _ (e.g. tiktok_shop)")
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	var p Platform
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(r.Context(), `INSERT INTO platforms (key, name, description) VALUES ($1, $2, $3) RETURNING id, created_at`,
		req.Key, name, strings.TrimSpace(req.Description)).Scan(&p.ID, &p.CreatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a platform with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// Every platform starts with its default metric group: metrics added
	// to it are reported for every experiment of every business under it.
	if err := createDefaultGroup(r.Context(), tx, p.ID); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	p.Groups = 1
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "platform", p.ID, "create", "", "", req)
	// Services resolve by platform and business: refresh the snapshot.
	_ = serving.Bump(r.Context(), s.DB)
	s.reload(r.Context())
	p.Key, p.Name, p.Description, p.Businesses = req.Key, name, strings.TrimSpace(req.Description), []Business{}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) UpdatePlatform(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "pid")
	var req platformRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE platforms SET name = $2, description = $3 WHERE id = $1`, id, name, strings.TrimSpace(req.Description))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "platform")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func createDefaultGroup(ctx context.Context, tx *sql.Tx, platformID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO metric_groups (platform_id, name, description, is_default, builtin, metric_ids)
		VALUES ($1, 'Default metrics', 'Applied to every experiment of every business on this platform', true, true, '{}')`, platformID)
	return err
}

// DeletePlatform removes an empty platform (and its definitions).
func (s *Server) DeletePlatform(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "pid")
	var n int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM businesses WHERE platform_id = $1`, id).Scan(&n); err != nil {
		serverError(w, r, err)
		return
	}
	if n > 0 {
		badRequest(w, fmt.Sprintf("the platform still has %d business(es); move or delete them first", n))
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM platforms WHERE id = $1`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "platform")
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "platform", id, "delete", "", "", nil)
	// Services resolve by platform and business: refresh the snapshot.
	_ = serving.Bump(r.Context(), s.DB)
	s.reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}
