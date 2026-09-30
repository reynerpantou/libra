// Package middleware provides HTTP middleware: security headers, session and
// API-key auth, roles, CSRF, rate limiting, panic recovery and logging.
package middleware

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/reynerpantou/libra/internal/auth"
)

const (
	SessionCookie = "libra_session"
	CSRFCookie    = "libra_csrf"
	CSRFHeader    = "X-CSRF-Token"
)

type ctxKey int

const (
	userKey ctxKey = iota
	apiKeyKey
)

// User is the signed-in person, as far as request handling needs to know.
type User struct {
	ID      int64
	Role    string // admin | editor | viewer
	IsOwner bool
}

// Rank orders roles so checks can say "at least editor".
func Rank(role string) int {
	switch role {
	case "admin":
		return 3
	case "editor":
		return 2
	case "viewer":
		return 1
	}
	return 0
}

// UserFrom returns the user set by RequireAuth.
func UserFrom(ctx context.Context) User {
	u, _ := ctx.Value(userKey).(User)
	return u
}

// SecurityHeaders sets conservative defaults for the SPA and API.
func SecurityHeaders(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self'; object-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
			h.Set("Permissions-Policy", "camera=(), geolocation=(), payment=(), usb=(), microphone=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if secure {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth validates the session cookie and injects the user.
func RequireAuth(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil {
				unauthorized(w)
				return
			}
			uid, err := auth.ValidateSession(db, c.Value)
			if err != nil {
				unauthorized(w)
				return
			}
			var u User
			if err := db.QueryRowContext(r.Context(), `SELECT id, role, is_owner FROM users WHERE id = $1`, uid).
				Scan(&u.ID, &u.Role, &u.IsOwner); err != nil {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
		})
	}
}

// RequireRole rejects users below the given role.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if Rank(UserFrom(r.Context()).Role) < Rank(role) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				http.Error(w, `{"code":"forbidden","message":"your role can't do this"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF enforces double-submit: mutating requests must echo the CSRF cookie
// in a header. Safe methods pass through.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(CSRFCookie)
		header := r.Header.Get(CSRFHeader)
		if err != nil || header == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			http.Error(w, `{"code":"csrf_error","message":"invalid csrf token"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	http.Error(w, `{"code":"unauthorized","message":"not signed in"}`, http.StatusUnauthorized)
}

// ---- API keys ----

// APIKeys authenticates services by key. Keys are looked up by hash and
// cached briefly, so the hot resolve path doesn't hit the database per call;
// a revoked key stops working within the cache TTL.
type APIKeys struct {
	db    *sql.DB
	mu    sync.Mutex
	cache map[string]apiKeyEntry
}

type apiKeyEntry struct {
	id       int64
	scopes   []string
	ok       bool
	loaded   time.Time
	lastUsed time.Time
}

const apiKeyTTL = 30 * time.Second

func NewAPIKeys(db *sql.DB) *APIKeys { return &APIKeys{db: db, cache: map[string]apiKeyEntry{}} }

// APIKeyID returns the id of the key that authenticated the request.
func APIKeyID(ctx context.Context) int64 {
	id, _ := ctx.Value(apiKeyKey).(int64)
	return id
}

// Require checks the request carries a live key with the given scope, sent
// as `Authorization: Bearer <key>` or `X-Libra-Key: <key>`.
func (k *APIKeys) Require(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-Libra-Key")
			if key == "" {
				key, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			key = strings.TrimSpace(key)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if key == "" || len(key) > 200 {
				http.Error(w, `{"code":"unauthorized","message":"missing API key"}`, http.StatusUnauthorized)
				return
			}
			e, err := k.lookup(r.Context(), auth.HashToken(key))
			if err != nil {
				http.Error(w, `{"code":"server_error","message":"could not check API key"}`, http.StatusInternalServerError)
				return
			}
			if !e.ok {
				http.Error(w, `{"code":"unauthorized","message":"invalid or revoked API key"}`, http.StatusUnauthorized)
				return
			}
			has := false
			for _, s := range e.scopes {
				if s == scope {
					has = true
				}
			}
			if !has {
				http.Error(w, `{"code":"forbidden","message":"API key lacks the `+scope+` scope"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), apiKeyKey, e.id)))
		})
	}
}

func (k *APIKeys) lookup(ctx context.Context, hash string) (apiKeyEntry, error) {
	now := time.Now()
	k.mu.Lock()
	e, ok := k.cache[hash]
	k.mu.Unlock()
	if !ok || now.Sub(e.loaded) > apiKeyTTL {
		var scopes string
		err := k.db.QueryRowContext(ctx,
			`SELECT id, array_to_string(scopes, ',') FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, hash,
		).Scan(&e.id, &scopes)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			e = apiKeyEntry{ok: false}
		case err != nil:
			return e, err
		default:
			e.ok, e.scopes = true, strings.Split(scopes, ",")
		}
		e.loaded = now
	}
	if e.ok && now.Sub(e.lastUsed) > time.Minute {
		e.lastUsed = now
		_, _ = k.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, e.id)
	}
	k.mu.Lock()
	if len(k.cache) > 10000 {
		k.cache = map[string]apiKeyEntry{}
	}
	k.cache[hash] = e
	k.mu.Unlock()
	return e, nil
}

// ---- general ----

// NoStore keeps API responses out of browser and proxy caches.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// RateLimit is a small in-memory sliding-window limiter keyed by client IP.
type RateLimit struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
	ip     func(*http.Request) string
}

func NewRateLimit(limit int, window time.Duration, ip func(*http.Request) string) *RateLimit {
	rl := &RateLimit{hits: map[string][]time.Time{}, limit: limit, window: window, ip: ip}
	go func() {
		for range time.Tick(window) {
			rl.mu.Lock()
			for k, v := range rl.hits {
				if len(v) == 0 || time.Since(v[len(v)-1]) > rl.window {
					delete(rl.hits, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *RateLimit) Wrap(next http.Handler) http.Handler { return rl.WrapWith(next, nil) }

// WrapWith is Wrap with a custom response when the limit is hit.
func (rl *RateLimit) WrapWith(next, limited http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := rl.ip(r)
		now := time.Now()
		rl.mu.Lock()
		recent := rl.hits[ip][:0:0]
		for _, t := range rl.hits[ip] {
			if now.Sub(t) < rl.window {
				recent = append(recent, t)
			}
		}
		if len(recent) >= rl.limit {
			rl.hits[ip] = recent
			rl.mu.Unlock()
			w.Header().Set("Retry-After", "60")
			if limited != nil {
				limited.ServeHTTP(w, r)
				return
			}
			http.Error(w, `{"code":"rate_limited","message":"too many attempts, try again later"}`, http.StatusTooManyRequests)
			return
		}
		rl.hits[ip] = append(recent, now)
		rl.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// Recover turns panics into 500s instead of dropping the connection.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				http.Error(w, `{"code":"server_error","message":"something went wrong"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Logger writes one line per request, skipping the high-volume runtime API.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.Contains(r.URL.Path, "/api/v1/") {
			return
		}
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

// ClientIP returns the visitor's IP. X-Forwarded-For is only believed when
// the connection comes from a trusted proxy, and it's read from the right,
// skipping trusted hops.
func ClientIP(trusted []netip.Prefix) func(*http.Request) string {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, err := netip.ParseAddr(host)
		if err != nil || !isTrusted(peer) {
			return host
		}
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			if !isTrusted(a) {
				return a.Unmap().String()
			}
		}
		return host
	}
}

// Chain applies middleware in order (first listed runs outermost).
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
