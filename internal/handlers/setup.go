package handlers

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reynerpantou/libra/internal/auth"
)

// Claiming the owner account. A new install has no accounts at all; the
// first owner is claimed through a one-time setup link rather than
// configured, the way Jenkins, GitLab or Grafana hand out their first admin:
//
//  1. While no owner can sign in, the server keeps one setup token (stored
//     hashed) and prints a link carrying it to its own log.
//  2. Whoever opens the link and signs in with Google or Apple becomes the
//     owner — their verified email and provider account are recorded.
//  3. The token is deleted. From then on the link is dead and everyone else
//     joins by invitation.
//
// Reading the server's log requires access to the server, so a stranger who
// finds the site first can't claim it.

const setupTTL = 24 * time.Hour

var errSetupDone = errors.New("libra already has an owner")

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// setupNeeded reports whether no owner can sign in: there's no owner, or
// the owner has neither an email nor a linked Google/Apple account.
func (s *Server) setupNeeded(ctx context.Context, q rowQuerier) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM users u WHERE u.is_owner
		  AND (u.email IS NOT NULL OR EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id)))`).Scan(&ok)
	return !ok, err
}

// NewSetupLink replaces any previous setup token and returns the link, or
// "" when an owner can already sign in.
func NewSetupLink(ctx context.Context, db *sql.DB, publicURL string) (string, error) {
	s := &Server{DB: db}
	needed, err := s.setupNeeded(ctx, db)
	if err != nil || !needed {
		return "", err
	}
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO owner_setup (id, token, expires_at) VALUES (true, $1, $2)
		 ON CONFLICT (id) DO UPDATE SET token = EXCLUDED.token, expires_at = EXCLUDED.expires_at`,
		auth.HashToken(token), time.Now().Add(setupTTL),
	); err != nil {
		return "", err
	}
	return publicURL + "/setup#" + token, nil
}

// SetupStart checks the setup token and begins a Google/Apple sign-in that
// will claim the owner account. It answers with the provider URL rather
// than redirecting, because the token arrives in a JSON body (it lives in
// the link's #fragment, which browsers never send to a server by
// themselves).
func (s *Server) SetupStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Provider string `json:"provider"`
	}
	if err := decode(r, &req); err != nil || req.Token == "" || len(req.Token) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing setup link")
		return
	}
	if needed, err := s.setupNeeded(r.Context(), s.DB); err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not check setup")
		return
	} else if !needed {
		writeError(w, http.StatusConflict, "setup_done", "libra already has an owner")
		return
	}
	var stored string
	var expires time.Time
	err := s.DB.QueryRowContext(r.Context(), `SELECT token, expires_at FROM owner_setup WHERE id`).Scan(&stored, &expires)
	if err != nil || time.Now().After(expires) ||
		subtle.ConstantTimeCompare([]byte(stored), []byte(auth.HashToken(req.Token))) != 1 {
		writeError(w, http.StatusUnauthorized, "setup_link_invalid", "this setup link is invalid or has expired")
		return
	}
	target, err := s.beginFlow(w, r, req.Provider, "setup")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "that sign-in option isn't available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": target})
}

// claimOwner makes the person who just signed in the owner: it fills in an
// existing owner that has no way to sign in, or creates the owner account.
// It re-checks inside a transaction, so only one claim can ever win.
func (s *Server) claimOwner(ctx context.Context, provider, subject, email string, verified bool, name string) (int64, error) {
	if email == "" || !verified {
		return 0, errNotInvited
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Serialize claims: two people finishing at once can't both win.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE owner_setup IN EXCLUSIVE MODE`); err != nil {
		return 0, err
	}
	var tokenLeft bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM owner_setup WHERE expires_at > now())`).Scan(&tokenLeft); err != nil {
		return 0, err
	}
	needed, err := s.setupNeeded(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !needed || !tokenLeft {
		return 0, errSetupDone
	}
	// The address may already belong to an invited account; it can't be
	// both that account and the owner.
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1) AND NOT is_owner)`, email).Scan(&taken); err != nil {
		return 0, err
	}
	if taken {
		return 0, fmt.Errorf("%s already belongs to another account", email)
	}

	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 80 {
		name = ""
	}
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&uid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		username, err := freeUsername(ctx, tx, email)
		if err != nil {
			return 0, err
		}
		display := name
		if display == "" {
			display = username
		}
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO users (username, email, display_name, role, is_owner) VALUES ($1, $2, $3, 'admin', true) RETURNING id`,
			username, email, display,
		).Scan(&uid); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE users SET email = $1, role = 'admin' WHERE id = $2`, email, uid); err != nil {
			return 0, err
		}
		if name != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET display_name = $1 WHERE id = $2 AND (display_name = '' OR display_name = username)`,
				name, uid); err != nil {
				return 0, err
			}
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM user_identities WHERE provider = $1 AND subject = $2`, []any{provider, subject}},
		{`DELETE FROM user_identities WHERE user_id = $1 AND provider = $2`, []any{uid, provider}},
		{`INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`, []any{provider, subject, uid, email}},
		{`DELETE FROM owner_setup`, nil},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return 0, err
		}
	}
	return uid, tx.Commit()
}

var usernameJunk = regexp.MustCompile(`[^a-z0-9._-]+`)

// freeUsername turns "Reyner.Pantou+x@gmail.com" into "reyner.pantou", with
// a number added if that's taken. An admin can rename it later.
func freeUsername(ctx context.Context, tx *sql.Tx, email string) (string, error) {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	local, _, _ = strings.Cut(local, "+")
	base := strings.Trim(usernameJunk.ReplaceAllString(local, ""), "._-")
	if len(base) > 32 {
		base = base[:32]
	}
	if base == "" {
		base = "owner"
	}
	for i := 1; i < 1000; i++ {
		candidate := base
		if i > 1 {
			candidate = fmt.Sprintf("%s%d", base, i)
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = $1)`, candidate).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("no free username")
}
