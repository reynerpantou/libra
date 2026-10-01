package handlers

import (
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Diversion types are the ids traffic can be split by. Adding one here
// makes it selectable for layers and auto layers; services then send it in
// the "ids" object of resolve, exposure and event requests.

type Diversion struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Builtin     bool      `json:"builtin"`
	Layers      int       `json:"layers"`           // layers (incl. dedicated ones) splitting by it
	Dedicated   int       `json:"dedicated_layers"` // of those, experiments' own (auto) layers
	CreatedAt   time.Time `json:"created_at"`
}

var diversionKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func (s *Server) ListDiversions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT d.key, d.name, d.description, d.builtin, d.created_at, (SELECT count(*) FROM layers l WHERE l.diversion = d.key),
		       (SELECT count(*) FROM layers l WHERE l.diversion = d.key AND l.auto)
		FROM diversions d ORDER BY d.builtin DESC, d.key`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Diversion{}
	for rows.Next() {
		var d Diversion
		if err := rows.Scan(&d.Key, &d.Name, &d.Description, &d.Builtin, &d.CreatedAt, &d.Layers, &d.Dedicated); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

type diversionRequest struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) CreateDiversion(w http.ResponseWriter, r *http.Request) {
	var req diversionRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if !diversionKey.MatchString(req.Key) {
		badRequest(w, "key must be lowercase letters, digits and _ (e.g. shop_id)")
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	_, err := s.DB.ExecContext(r.Context(), `INSERT INTO diversions (key, name, description) VALUES ($1, $2, $3)`, req.Key, name, strings.TrimSpace(req.Description))
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "that diversion type exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "diversion", 0, "create", "", "", req)
	writeJSON(w, http.StatusCreated, Diversion{Key: req.Key, Name: name, Description: req.Description, CreatedAt: time.Now().UTC()})
}

func (s *Server) UpdateDiversion(w http.ResponseWriter, r *http.Request) {
	var req diversionRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE diversions SET name = $2, description = $3 WHERE key = $1`, r.PathValue("key"), name, strings.TrimSpace(req.Description))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "diversion type")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteDiversion(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var builtin bool
	var layers int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT builtin, (SELECT count(*) FROM layers WHERE diversion = $1) FROM diversions WHERE key = $1`, key).Scan(&builtin, &layers); err != nil {
		notFound(w, "diversion type")
		return
	}
	switch {
	case builtin:
		badRequest(w, "user_id and device_id are built in")
		return
	case layers > 0:
		badRequest(w, "layers still split by this; change or remove them first")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM diversions WHERE key = $1`, key); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
