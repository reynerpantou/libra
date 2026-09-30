package handlers

import (
	"database/sql"
	"errors"
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

func (s *Server) ListPlatforms(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT p.id, p.key, p.name, p.description, p.created_at,
		       (SELECT count(*) FROM measures WHERE platform_id = p.id),
		       (SELECT count(*) FROM metrics WHERE platform_id = p.id),
		       (SELECT count(*) FROM metric_groups WHERE platform_id = p.id)
		FROM platforms p ORDER BY p.name`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var out []*Platform
	by := map[int64]*Platform{}
	for rows.Next() {
		p := &Platform{Businesses: []Business{}}
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.Measures, &p.Metrics, &p.Groups); err != nil {
			rows.Close()
			serverError(w, r, err)
			return
		}
		out = append(out, p)
		by[p.ID] = p
	}
	rows.Close()
	brows, err := s.DB.QueryContext(r.Context(), `
		SELECT b.id, b.platform_id, b.key, b.name, b.description, b.require_review, b.created_at,
		       (SELECT count(*) FROM measures WHERE business_id = b.id),
		       (SELECT count(*) FROM metrics WHERE business_id = b.id),
		       (SELECT count(*) FROM experiments WHERE business_id = b.id AND status <> 'archived')
		FROM businesses b ORDER BY b.name`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer brows.Close()
	for brows.Next() {
		var b Business
		if err := brows.Scan(&b.ID, &b.PlatformID, &b.Key, &b.Name, &b.Description, &b.RequireReview, &b.CreatedAt, &b.Measures, &b.Metrics, &b.Experiments); err != nil {
			serverError(w, r, err)
			return
		}
		if p := by[b.PlatformID]; p != nil {
			b.PlatformName = p.Name
			p.Businesses = append(p.Businesses, b)
		}
	}
	if out == nil {
		out = []*Platform{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetPlatform(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "pid")
	var p Platform
	err := s.DB.QueryRowContext(r.Context(), `SELECT id, key, name, description, created_at FROM platforms WHERE id = $1`, id).
		Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "platform")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	p.Businesses = []Business{}
	writeJSON(w, http.StatusOK, p)
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
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO platforms (key, name, description) VALUES ($1, $2, $3) RETURNING id, created_at`,
		req.Key, name, strings.TrimSpace(req.Description)).Scan(&p.ID, &p.CreatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a platform with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "platform", p.ID, "create", "", "", req)
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
