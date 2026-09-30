// Package sso signs people in with Google and Apple (OpenID Connect,
// authorization-code flow). It only proves who someone is; deciding whether
// that person may use Libra is the caller's job.
package sso

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Claims is what Libra uses from a verified ID token.
type Claims struct {
	Subject       string // the provider's permanent id for this person
	Email         string
	EmailVerified bool
	Name          string
}

type Provider struct {
	Name     string // "google" or "apple"
	ClientID string
	AuthURL  string
	TokenURL string
	JWKSURL  string
	Issuers  []string
	Scope    string
	// FormPost: the provider returns to the callback with a cross-site POST
	// (Apple does whenever name or email is requested).
	FormPost bool
	PKCE     bool

	secret func() (string, error)
	keys   keyCache
	client *http.Client
}

// Endpoints overrides where a provider lives. Only tests use it.
type Endpoints struct{ AuthURL, TokenURL, JWKSURL, Issuer string }

func endpointsFrom(base string, def Endpoints) Endpoints {
	if base == "" {
		return def
	}
	base = strings.TrimRight(base, "/")
	return Endpoints{AuthURL: base + "/auth", TokenURL: base + "/token", JWKSURL: base + "/keys", Issuer: base}
}

// NewGoogle configures Sign in with Google. testBase is empty in production.
func NewGoogle(clientID, clientSecret, testBase string) *Provider {
	e := endpointsFrom(testBase, Endpoints{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		JWKSURL:  "https://www.googleapis.com/oauth2/v3/certs",
		Issuer:   "https://accounts.google.com",
	})
	issuers := []string{e.Issuer}
	if testBase == "" {
		issuers = append(issuers, "accounts.google.com")
	}
	return &Provider{
		Name: "google", ClientID: clientID,
		AuthURL: e.AuthURL, TokenURL: e.TokenURL, JWKSURL: e.JWKSURL, Issuers: issuers,
		Scope: "openid email profile", PKCE: true,
		secret: func() (string, error) { return clientSecret, nil },
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewApple configures Sign in with Apple. Apple has no fixed client secret:
// each token request carries a short-lived JWT signed with the .p8 key from
// the Apple Developer account.
func NewApple(clientID, teamID, keyID, privateKeyPEM, testBase string) (*Provider, error) {
	key, err := parseP8(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	e := endpointsFrom(testBase, Endpoints{
		AuthURL:  "https://appleid.apple.com/auth/authorize",
		TokenURL: "https://appleid.apple.com/auth/token",
		JWKSURL:  "https://appleid.apple.com/auth/keys",
		Issuer:   "https://appleid.apple.com",
	})
	return &Provider{
		Name: "apple", ClientID: clientID,
		AuthURL: e.AuthURL, TokenURL: e.TokenURL, JWKSURL: e.JWKSURL, Issuers: []string{e.Issuer},
		Scope: "name email", FormPost: true,
		secret: func() (string, error) {
			now := time.Now()
			return signES256(key, keyID, map[string]any{
				"iss": teamID, "sub": clientID, "aud": "https://appleid.apple.com",
				"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
			})
		},
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// AuthCodeURL is where the browser is sent to sign in.
func (p *Provider) AuthCodeURL(redirectURI, state, nonce, verifier string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {p.ClientID},
		"redirect_uri":  {redirectURI},
		"scope":         {p.Scope},
		"state":         {state},
		"nonce":         {nonce},
	}
	if p.FormPost {
		q.Set("response_mode", "form_post")
	}
	if p.PKCE {
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	if p.Name == "google" {
		q.Set("prompt", "select_account")
	}
	return p.AuthURL + "?" + q.Encode()
}

// Exchange trades the one-time code for an ID token and verifies it.
func (p *Provider) Exchange(ctx context.Context, code, redirectURI, verifier, nonce string) (Claims, error) {
	secret, err := p.secret()
	if err != nil {
		return Claims{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {p.ClientID},
		"client_secret": {secret},
	}
	if p.PKCE {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return Claims{}, fmt.Errorf("%s token request: %w", p.Name, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &tok)
	if res.StatusCode != http.StatusOK || tok.IDToken == "" {
		return Claims{}, fmt.Errorf("%s token request: status %d %s", p.Name, res.StatusCode, tok.Error)
	}
	return p.Verify(ctx, tok.IDToken, nonce)
}

// Verify checks an ID token's signature against the provider's published
// keys, and that it was issued by this provider, for Libra, recently, and
// for this very sign-in (nonce).
func (p *Provider) Verify(ctx context.Context, idToken, nonce string) (Claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed id token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Claims{}, err
	}
	if header.Alg != "RS256" {
		return Claims{}, fmt.Errorf("unexpected id token algorithm %q", header.Alg)
	}
	key, err := p.keys.get(ctx, p.client, p.JWKSURL, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("malformed id token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return Claims{}, errors.New("id token signature is invalid")
	}

	var c struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Exp           int64           `json:"exp"`
		Iat           int64           `json:"iat"`
		Nonce         string          `json:"nonce"`
		Sub           string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"` // Apple sends "true" as a string
		Name          string          `json:"name"`
	}
	if err := decodeSegment(parts[1], &c); err != nil {
		return Claims{}, err
	}
	now := time.Now().Unix()
	const skew = 120
	switch {
	case !slices.Contains(p.Issuers, c.Iss):
		return Claims{}, fmt.Errorf("unexpected issuer %q", c.Iss)
	case !audienceHas(c.Aud, p.ClientID):
		return Claims{}, errors.New("id token is for another app")
	case c.Exp+skew < now:
		return Claims{}, errors.New("id token expired")
	case c.Iat-skew > now:
		return Claims{}, errors.New("id token issued in the future")
	case nonce == "" || c.Nonce != nonce:
		return Claims{}, errors.New("id token nonce mismatch")
	case c.Sub == "":
		return Claims{}, errors.New("id token has no subject")
	}
	verified := c.EmailVerified == true || c.EmailVerified == "true"
	return Claims{Subject: c.Sub, Email: strings.ToLower(strings.TrimSpace(c.Email)), EmailVerified: verified, Name: c.Name}, nil
}

func audienceHas(raw json.RawMessage, clientID string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == clientID
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, clientID)
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return errors.New("malformed id token")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errors.New("malformed id token")
	}
	return nil
}

// keyCache holds a provider's signing keys. They rotate, so an unknown key
// id triggers a refetch — at most once a minute, so bogus tokens can't turn
// Libra into a request flood against the provider.
type keyCache struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (kc *keyCache) get(ctx context.Context, client *http.Client, jwksURL, kid string) (*rsa.PublicKey, error) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if k, ok := kc.keys[kid]; ok && time.Since(kc.fetched) < 24*time.Hour {
		return k, nil
	}
	if time.Since(kc.fetched) > time.Minute {
		keys, err := fetchJWKS(ctx, client, jwksURL)
		if err != nil {
			return nil, err
		}
		kc.keys, kc.fetched = keys, time.Now()
	}
	if k, ok := kc.keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("id token signed with an unknown key")
}

func fetchJWKS(ctx context.Context, client *http.Client, jwksURL string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch signing keys: status %d", res.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("read signing keys: %w", err)
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return out, nil
}

// parseP8 reads Apple's .p8 key, either as the whole file or as just the
// lines between BEGIN and END joined into one (easier to put in .env).
func parseP8(text string) (*ecdsa.PrivateKey, error) {
	var der []byte
	if block, _ := pem.Decode([]byte(text)); block != nil {
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
		if err != nil {
			return nil, errors.New("apple private key: not a .p8 key")
		}
		der = b
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("apple private key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple private key: not an EC key")
	}
	return ec, nil
}

// signES256 produces a compact JWS (the raw r||s signature, not DER).
func signES256(key *ecdsa.PrivateKey, keyID string, claims map[string]any) (string, error) {
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"})
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
