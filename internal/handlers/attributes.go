package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/reynerpantou/libra/internal/assign"
)

// Targeting attributes are managed here, so targeting rules pick from a
// known list (with types and suggested values) instead of free text.

type Attribute struct {
	ID          int64    `json:"id"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"` // string | number | version | boolean
	Options     []string `json:"options"`
	UsedBy      int      `json:"used_by"` // experiments (not archived) targeting on it
}

var attributeKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var attributeTypes = map[string]bool{"string": true, "number": true, "version": true, "boolean": true}

func (s *Server) attributeKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key FROM attributes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// attributeUsage counts, per attribute, the experiments targeting on it.
func (s *Server) attributeUsage(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT targeting FROM experiments WHERE status <> 'archived'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var t assign.Targeting
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		for _, a := range t.Attrs() {
			out[a]++
		}
	}
	return out, rows.Err()
}

func (s *Server) ListAttributes(w http.ResponseWriter, r *http.Request) {
	usage, err := s.attributeUsage(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, key, name, description, type, array_to_string(options, E'\x1f') FROM attributes ORDER BY key`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Attribute{}
	for rows.Next() {
		var a Attribute
		var opts string
		if err := rows.Scan(&a.ID, &a.Key, &a.Name, &a.Description, &a.Type, &opts); err != nil {
			serverError(w, r, err)
			return
		}
		a.Options = []string{}
		if opts != "" {
			a.Options = strings.Split(opts, "\x1f")
		}
		a.UsedBy = usage[a.Key]
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

type attributeRequest struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Options     []string `json:"options"`
}

func (req *attributeRequest) clean() string {
	req.Key = strings.TrimSpace(req.Key)
	name, ok := trimmed(req.Name, 80)
	if !ok {
		return "name is required"
	}
	req.Name = name
	req.Description = strings.TrimSpace(req.Description)
	if !attributeTypes[req.Type] {
		return "type must be string, number, version or boolean"
	}
	seen := map[string]bool{}
	opts := []string{}
	for _, o := range req.Options {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		if len(o) > 100 {
			return "options can be at most 100 characters"
		}
		seen[o] = true
		opts = append(opts, o)
	}
	if len(opts) > 500 {
		return "at most 500 options"
	}
	req.Options = opts
	return ""
}

func (s *Server) CreateAttribute(w http.ResponseWriter, r *http.Request) {
	var req attributeRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := req.clean(); msg != "" {
		badRequest(w, msg)
		return
	}
	if !attributeKey.MatchString(req.Key) {
		badRequest(w, "key must be lowercase letters, digits and _ (e.g. app_version)")
		return
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(),
		`INSERT INTO attributes (key, name, description, type, options) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		req.Key, req.Name, req.Description, req.Type, req.Options).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "an attribute with that key exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "attribute", id, "create", "", "", req)
	writeJSON(w, http.StatusCreated, Attribute{ID: id, Key: req.Key, Name: req.Name, Description: req.Description, Type: req.Type, Options: req.Options})
}

// UpdateAttribute changes everything but the key (rules refer to it).
func (s *Server) UpdateAttribute(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req attributeRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := req.clean(); msg != "" {
		badRequest(w, msg)
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE attributes SET name = $2, description = $3, type = $4, options = $5 WHERE id = $1`,
		id, req.Name, req.Description, req.Type, req.Options)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, "attribute")
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "attribute", id, "update", "", "", req)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteAttribute(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var key string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT key FROM attributes WHERE id = $1`, id).Scan(&key); err != nil {
		notFound(w, "attribute")
		return
	}
	usage, err := s.attributeUsage(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n := usage[key]; n > 0 {
		badRequest(w, "experiments still target on this attribute; change them first")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM attributes WHERE id = $1`, id); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "attribute", id, "delete", "", "", map[string]string{"key": key})
	w.WriteHeader(http.StatusNoContent)
}

// DiscoveredAttributes lists request attributes seen in recent traffic that
// aren't registered yet, with sample values — one click to register them.
func (s *Server) DiscoveredAttributes(w http.ResponseWriter, r *http.Request) {
	known, err := s.attributeKeys(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT key, value, count(*) FROM (
			SELECT attrs FROM exposures ORDER BY id DESC LIMIT 20000
		) x, jsonb_each_text(x.attrs)
		GROUP BY key, value ORDER BY key, count(*) DESC`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type found struct {
		Key    string   `json:"key"`
		Seen   int64    `json:"seen"`
		Values []string `json:"values"`
	}
	by := map[string]*found{}
	for rows.Next() {
		var k, v string
		var n int64
		if err := rows.Scan(&k, &v, &n); err != nil {
			serverError(w, r, err)
			return
		}
		if known[k] {
			continue
		}
		f := by[k]
		if f == nil {
			f = &found{Key: k, Values: []string{}}
			by[k] = f
		}
		f.Seen += n
		if len(f.Values) < 20 {
			f.Values = append(f.Values, v)
		}
	}
	out := []*found{}
	for _, f := range by {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seen > out[j].Seen })
	writeJSON(w, http.StatusOK, out)
}
