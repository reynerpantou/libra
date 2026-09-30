package handlers

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reynerpantou/libra/internal/auth"
	"github.com/reynerpantou/libra/internal/middleware"
)

// Signing in works by invitation: an administrator enters a person's email
// address, and whoever proves that address through Google or Apple gets in.
// On that first sign-in the provider's permanent id for the person is
// linked to the account, and later sign-ins go by that id. Libra never
// sees or stores a password.

const (
	flowCookie = "libra_sso"
	flowTTL    = 10 * time.Minute
	linkTTL    = 15 * time.Minute
)

var providerOrder = []string{"google", "apple"}

// AuthProviders lists the sign-in buttons the login page should show.
func (s *Server) AuthProviders(w http.ResponseWriter, r *http.Request) {
	out := []string{}
	for _, name := range providerOrder {
		if s.Providers[name] != nil {
			out = append(out, name)
		}
	}
	needed, _ := s.setupNeeded(r.Context(), s.DB)
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "setup_needed": needed})
}

func (s *Server) redirectURI(provider string) string {
	return s.Cfg.PublicURL + "/api/auth/" + provider + "/callback"
}

// loginError sends the browser back to the sign-in page with a reason code
// the page translates.
func loginError(w http.ResponseWriter, r *http.Request, code string, extra url.Values) {
	q := url.Values{"error": {code}}
	for k, v := range extra {
		q[k] = v
	}
	http.Redirect(w, r, "/login?"+q.Encode(), http.StatusSeeOther)
}

// AuthStart begins a sign-in and sends the browser to the provider.
func (s *Server) AuthStart(w http.ResponseWriter, r *http.Request) {
	target, err := s.beginFlow(w, r, r.PathValue("provider"), "signin")
	if err != nil {
		loginError(w, r, err.Error(), nil)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// beginFlow remembers a one-time state, nonce and PKCE verifier for this
// browser and returns the provider URL to send it to. On failure the error
// text is a login error code.
func (s *Server) beginFlow(w http.ResponseWriter, r *http.Request, name, purpose string) (string, error) {
	p := s.Providers[name]
	if p == nil {
		return "", errors.New("unavailable")
	}
	state, err1 := auth.NewToken()
	nonce, err2 := auth.NewToken()
	verifier, err3 := auth.NewToken()
	browser, err4 := auth.NewToken()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return "", errors.New("failed")
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`INSERT INTO auth_flows (id, browser, provider, nonce, verifier, expires_at, purpose) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		auth.HashToken(state), auth.HashToken(browser), name, nonce, verifier, time.Now().Add(flowTTL), purpose,
	); err != nil {
		log.Printf("sso start: %v", err)
		return "", errors.New("failed")
	}
	// Apple comes back with a cross-site POST, which only carries cookies
	// marked SameSite=None (and those must be Secure). Google comes back
	// with a normal redirect, where Lax is enough.
	sameSite := http.SameSiteLaxMode
	if p.FormPost && s.Cfg.CookieSecure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: browser, Path: "/api/auth/",
		HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: sameSite,
		MaxAge: int(flowTTL.Seconds()),
	})
	return p.AuthCodeURL(s.redirectURI(name), state, nonce, verifier), nil
}

// AuthCallback finishes a sign-in. Google calls it with GET, Apple with a
// form POST. Every check happens here, server to server; nothing the
// browser says about who it is is trusted.
func (s *Server) AuthCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p := s.Providers[name]
	if p == nil {
		loginError(w, r, "unavailable", nil)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			loginError(w, r, "failed", nil)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/api/auth/", MaxAge: -1, HttpOnly: true, Secure: s.Cfg.CookieSecure})
	if e := r.FormValue("error"); e != "" {
		if e == "access_denied" || e == "user_cancelled_authorize" {
			loginError(w, r, "cancelled", nil)
		} else {
			loginError(w, r, "failed", nil)
		}
		return
	}
	state, code := r.FormValue("state"), r.FormValue("code")
	c, err := r.Cookie(flowCookie)
	if state == "" || code == "" || err != nil {
		loginError(w, r, "expired", nil)
		return
	}

	// The flow is deleted as it's read, so a state works exactly once.
	var browser, provider, nonce, verifier, purpose string
	var expires time.Time
	err = s.DB.QueryRowContext(r.Context(),
		`DELETE FROM auth_flows WHERE id = $1 RETURNING browser, provider, nonce, verifier, expires_at, purpose`,
		auth.HashToken(state),
	).Scan(&browser, &provider, &nonce, &verifier, &expires, &purpose)
	if err != nil || provider != name || time.Now().After(expires) ||
		subtle.ConstantTimeCompare([]byte(browser), []byte(auth.HashToken(c.Value))) != 1 {
		loginError(w, r, "expired", nil)
		return
	}

	claims, err := p.Exchange(r.Context(), code, s.redirectURI(name), verifier, nonce)
	if err != nil {
		log.Printf("sso %s: %v", name, err)
		loginError(w, r, "failed", nil)
		return
	}
	// Apple sends the person's name only on their very first sign-in, as a
	// form field next to the code (not inside the signed token).
	if claims.Name == "" && name == "apple" {
		claims.Name = appleName(r.FormValue("user"))
	}

	var uid int64
	if purpose == "setup" {
		uid, err = s.claimOwner(r.Context(), name, claims.Subject, claims.Email, claims.EmailVerified, claims.Name)
		if errors.Is(err, errSetupDone) {
			loginError(w, r, "setup_done", nil)
			return
		}
	} else {
		uid, err = s.linkIdentity(r.Context(), name, claims.Subject, claims.Email, claims.EmailVerified, claims.Name)
	}
	if errors.Is(err, errNotInvited) {
		extra := url.Values{}
		if claims.Email != "" {
			extra.Set("email", claims.Email)
		}
		loginError(w, r, "not_invited", extra)
		return
	}
	if err != nil {
		log.Printf("sso %s link: %v", name, err)
		loginError(w, r, "failed", nil)
		return
	}
	if err := s.startSession(w, uid); err != nil {
		loginError(w, r, "failed", nil)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

var errNotInvited = errors.New("no account for this sign-in")

// linkIdentity finds the account a provider identity belongs to. A known
// identity signs straight in. An unknown one is linked to the account whose
// invitation email it proves — only if the provider verified that email.
func (s *Server) linkIdentity(ctx context.Context, provider, subject, email string, verified bool, name string) (int64, error) {
	var uid int64
	err := s.DB.QueryRowContext(ctx,
		`UPDATE user_identities SET last_used = now(), email = $3 WHERE provider = $1 AND subject = $2 RETURNING user_id`,
		provider, subject, email,
	).Scan(&uid)
	if err == nil {
		return uid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if email == "" || !verified {
		return 0, errNotInvited
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var username, display string
	err = tx.QueryRowContext(ctx, `SELECT id, username, display_name FROM users WHERE lower(email) = lower($1) FOR UPDATE`, email).
		Scan(&uid, &username, &display)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errNotInvited
	}
	if err != nil {
		return 0, err
	}
	// One account per provider: a new Google account proving the same email
	// (say, the old one was deleted) replaces the previous link.
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id = $1 AND provider = $2`, uid, provider); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`,
		provider, subject, uid, email,
	); err != nil {
		return 0, err
	}
	// Accounts created in Administration are named after their username;
	// the first sign-in gives them the person's real name instead.
	name = strings.TrimSpace(name)
	if name != "" && utf8.RuneCountInString(name) <= 80 && (display == username || display == "") {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET display_name = $1 WHERE id = $2`, name, uid); err != nil {
			return 0, err
		}
	}
	return uid, tx.Commit()
}

func appleName(userJSON string) string {
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if userJSON == "" || json.Unmarshal([]byte(userJSON), &u) != nil {
		return ""
	}
	return strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
}

// startSession signs the browser in as uid.
func (s *Server) startSession(w http.ResponseWriter, uid int64) error {
	token, err := auth.CreateSession(s.DB, uid, s.Cfg.SessionTTL)
	if err != nil {
		return err
	}
	csrf, err := auth.NewToken()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: middleware.SessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(s.Cfg.SessionTTL),
	})
	http.SetCookie(w, &http.Cookie{
		Name: middleware.CSRFCookie, Value: csrf, Path: "/",
		HttpOnly: false, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(s.Cfg.SessionTTL),
	})
	return nil
}

// AuthLink redeems a one-time sign-in link made on the server with
// `libra sign-in-link`. The token travels in the URL fragment, which
// browsers never send anywhere, and is posted here as JSON — so link
// previews and prefetchers can't use it up.
func (s *Server) AuthLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decode(r, &req); err != nil || req.Token == "" || len(req.Token) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing sign-in link")
		return
	}
	var uid int64
	var expires time.Time
	err := s.DB.QueryRowContext(r.Context(),
		`DELETE FROM sign_in_links WHERE token = $1 RETURNING user_id, expires_at`, auth.HashToken(req.Token),
	).Scan(&uid, &expires)
	if err != nil || time.Now().After(expires) {
		writeError(w, http.StatusUnauthorized, "link_expired", "this sign-in link is invalid or has expired")
		return
	}
	if err := s.startSession(w, uid); err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not start your session")
		return
	}
	u, err := s.loadUser(uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not load your account")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// NewSignInLink stores a one-time link for uid and returns its URL.
func NewSignInLink(db *sql.DB, publicURL string, uid int64) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`INSERT INTO sign_in_links (token, user_id, expires_at) VALUES ($1, $2, $3)`,
		auth.HashToken(token), uid, time.Now().Add(linkTTL)); err != nil {
		return "", err
	}
	return publicURL + "/login/link#" + token, nil
}

// linkedProviders lists which sign-in accounts are connected to uid.
func (s *Server) linkedProviders(ctx context.Context, uid int64) []string {
	out := []string{}
	rows, err := s.DB.QueryContext(ctx, `SELECT provider FROM user_identities WHERE user_id = $1`, uid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(providerOrder, a) - slices.Index(providerOrder, b) })
	return out
}
