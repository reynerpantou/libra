package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/auth"
	"github.com/reynerpantou/libra/internal/config"
	"github.com/reynerpantou/libra/internal/middleware"
)

type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	IsOwner     bool      `json:"is_owner"`
	CreatedAt   time.Time `json:"created_at"`
	Linked      []string  `json:"linked,omitempty"`
}

func (s *Server) loadUser(id int64) (User, error) {
	var u User
	err := s.DB.QueryRow(`SELECT id, username, COALESCE(email, ''), display_name, role, is_owner, created_at FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.IsOwner, &u.CreatedAt)
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	return u, err
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(middleware.SessionCookie); err == nil {
		_ = auth.DeleteSession(s.DB, c.Value)
	}
	for _, name := range []string{middleware.SessionCookie, middleware.CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: config.BasePath + "/", MaxAge: -1, HttpOnly: name == middleware.SessionCookie, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	u, err := s.loadUser(user(r).ID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
		return
	}
	u.Linked = s.linkedProviders(r.Context(), u.ID)
	writeJSON(w, http.StatusOK, u)
}

// ListUsers is visible to everyone signed in (to pick owners and read
// history); emails are only shown to admins.
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, username, COALESCE(email, ''), display_name, role, is_owner, created_at FROM users ORDER BY lower(username)`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	admin := user(r).Role == "admin"
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.IsOwner, &u.CreatedAt); err != nil {
			serverError(w, r, err)
			return
		}
		if u.DisplayName == "" {
			u.DisplayName = u.Username
		}
		if !admin {
			u.Email = ""
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type userRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func validRole(r string) bool { return r == "admin" || r == "editor" || r == "viewer" }

func normalizeEmail(e string) (string, bool) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "", true
	}
	a, err := mail.ParseAddress(e)
	return e, err == nil && a.Address == e && len(e) <= 254
}

// CreateUser invites someone: whoever proves the email through Google or
// Apple signs in as this account.
func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	username, ok := trimmed(req.Username, 64)
	if !ok || strings.ContainsAny(username, " \t\n") {
		badRequest(w, "username is required, up to 64 characters, no spaces")
		return
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		badRequest(w, "enter a valid email address")
		return
	}
	if !validRole(req.Role) {
		badRequest(w, "role must be admin, editor or viewer")
		return
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	var id int64
	err := s.DB.QueryRowContext(r.Context(),
		`INSERT INTO users (username, email, display_name, role) VALUES ($1, $2, $3, $4) RETURNING id`,
		username, emailArg, strings.TrimSpace(req.DisplayName), req.Role).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "that username or email is already used")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	u, _ := s.loadUser(id)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, "user")
		return
	}
	var req userRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	target, err := s.loadUser(id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "user")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !validRole(req.Role) {
		badRequest(w, "role must be admin, editor or viewer")
		return
	}
	if target.IsOwner && req.Role != "admin" {
		badRequest(w, "the owner is always an admin")
		return
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		badRequest(w, "enter a valid email address")
		return
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET email = $2, display_name = $3, role = $4 WHERE id = $1`,
		id, emailArg, strings.TrimSpace(req.DisplayName), req.Role); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "that email is already used")
			return
		}
		serverError(w, r, err)
		return
	}
	// A changed email means a different person signs in: drop old links.
	if !strings.EqualFold(email, target.Email) {
		for _, q := range []string{`DELETE FROM user_identities WHERE user_id = $1`, `DELETE FROM sessions WHERE user_id = $1`} {
			if _, err := tx.ExecContext(r.Context(), q, id); err != nil {
				serverError(w, r, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	u, _ := s.loadUser(id)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, "user")
		return
	}
	if id == user(r).ID {
		badRequest(w, "you can't delete your own account")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM users WHERE id = $1 AND NOT is_owner`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		badRequest(w, "that account doesn't exist or is the owner")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- API keys ----

type APIKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Key        string     `json:"key,omitempty"` // only in the create response
}

var apiScopes = map[string]bool{"runtime": true, "ingest": true}

func (s *Server) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT k.id, k.name, k.prefix, array_to_string(k.scopes, ','), COALESCE(u.username, ''), k.created_at, k.last_used_at, k.revoked_at
		FROM api_keys k LEFT JOIN users u ON u.id = k.created_by ORDER BY k.revoked_at IS NOT NULL, k.id DESC`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var scopes string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &scopes, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
			serverError(w, r, err)
			return
		}
		k.Scopes = strings.Split(scopes, ",")
		out = append(out, k)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "give the key a name (up to 80 characters)")
		return
	}
	if len(req.Scopes) == 0 {
		badRequest(w, "pick at least one scope")
		return
	}
	for _, sc := range req.Scopes {
		if !apiScopes[sc] {
			badRequest(w, "scopes are runtime and ingest")
			return
		}
	}
	key, err := NewAPIKey(r.Context(), s.DB, name, req.Scopes, user(r).ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

// NewAPIKey creates a key and returns it with the secret (shown once).
func NewAPIKey(ctx context.Context, db *sql.DB, name string, scopes []string, createdBy int64) (APIKey, error) {
	tok, err := auth.NewToken()
	if err != nil {
		return APIKey{}, err
	}
	secret := "lk_" + tok
	k := APIKey{Name: name, Prefix: secret[:10], Scopes: scopes, Key: secret, CreatedAt: time.Now().UTC()}
	var by any
	if createdBy > 0 {
		by = createdBy
	}
	err = db.QueryRowContext(ctx, `INSERT INTO api_keys (name, prefix, key_hash, scopes, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		name, k.Prefix, auth.HashToken(secret), scopes, by).Scan(&k.ID)
	return k, err
}

func (s *Server) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, "API key")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
