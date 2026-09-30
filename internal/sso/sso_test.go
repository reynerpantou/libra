package sso

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	d := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()
	p := NewGoogle("client-1", "secret", jwks.URL)
	p.JWKSURL = jwks.URL

	now := time.Now().Unix()
	good := func() map[string]any {
		return map[string]any{"iss": jwks.URL, "aud": "client-1", "exp": now + 300, "iat": now,
			"nonce": "n1", "sub": "123", "email": "Person@Gmail.com", "email_verified": true, "name": "Pat"}
	}
	hdr := map[string]any{"alg": "RS256", "kid": "k1"}
	ctx := context.Background()

	c, err := p.Verify(ctx, sign(t, key, hdr, good()), "n1")
	if err != nil || c.Subject != "123" || c.Email != "person@gmail.com" || !c.EmailVerified || c.Name != "Pat" {
		t.Fatalf("valid token rejected: %v %+v", err, c)
	}

	apple := good()
	apple["email_verified"] = "true"
	apple["aud"] = []string{"client-1"}
	if c, err := p.Verify(ctx, sign(t, key, hdr, apple), "n1"); err != nil || !c.EmailVerified {
		t.Fatalf("string email_verified / array aud rejected: %v", err)
	}

	bad := map[string]func() (map[string]any, map[string]any, *rsa.PrivateKey, string){
		"wrong key": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) { return hdr, good(), other, "n1" },
		"alg none": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			return map[string]any{"alg": "none", "kid": "k1"}, good(), key, "n1"
		},
		"unknown kid": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			return map[string]any{"alg": "RS256", "kid": "zz"}, good(), key, "n1"
		},
		"wrong audience": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			c := good()
			c["aud"] = "someone-else"
			return hdr, c, key, "n1"
		},
		"wrong issuer": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			c := good()
			c["iss"] = "https://evil.example"
			return hdr, c, key, "n1"
		},
		"expired": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			c := good()
			c["exp"] = now - 3600
			return hdr, c, key, "n1"
		},
		"nonce mismatch": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) { return hdr, good(), key, "n2" },
		"empty nonce": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			c := good()
			c["nonce"] = ""
			return hdr, c, key, ""
		},
		"no subject": func() (map[string]any, map[string]any, *rsa.PrivateKey, string) {
			c := good()
			delete(c, "sub")
			return hdr, c, key, "n1"
		},
	}
	for name, mk := range bad {
		h, c, k, nonce := mk()
		if _, err := p.Verify(ctx, sign(t, k, h, c), nonce); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A token whose payload was edited after signing.
	tok := sign(t, key, hdr, good())
	parts := strings.Split(tok, ".")
	forged := good()
	forged["sub"] = "999"
	fb, _ := json.Marshal(forged)
	parts[1] = base64.RawURLEncoding.EncodeToString(fb)
	if _, err := p.Verify(ctx, strings.Join(parts, "."), "n1"); err == nil {
		t.Error("edited payload accepted")
	}
}

func TestAppleClientSecret(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	p, err := NewApple("com.example.web", "TEAM123", "KEY123", pemText, "")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := p.secret()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %s", secret)
	}
	var h, c map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &c)
	if h["alg"] != "ES256" || h["kid"] != "KEY123" || c["iss"] != "TEAM123" || c["sub"] != "com.example.web" || c["aud"] != "https://appleid.apple.com" {
		t.Fatalf("bad client secret: %v %v", h, c)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, d[:], r, s) {
		t.Fatal("client secret signature doesn't verify")
	}
	oneLine := base64.StdEncoding.EncodeToString(der)
	if _, err := NewApple("x", "t", "k", oneLine, ""); err != nil {
		t.Errorf("one-line key rejected: %v", err)
	}
	if _, err := NewApple("x", "t", "k", "not a key", ""); err == nil {
		t.Error("garbage key accepted")
	}
}
