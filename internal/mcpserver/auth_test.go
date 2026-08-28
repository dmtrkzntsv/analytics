package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type jwksFixture struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
	issuer string
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &jwksFixture{key: key, kid: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.issuer, "jwks_uri": f.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := f.key.Public().(*rsa.PublicKey)
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": f.kid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	f.server = httptest.NewServer(mux)
	f.issuer = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

func (f *jwksFixture) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *jwksFixture) claims(over jwt.MapClaims) jwt.MapClaims {
	c := jwt.MapClaims{
		"iss": f.issuer, "aud": "https://analytics.example.com/mcp",
		"sub": "user@example.com", "exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

func TestOAuthVerifier(t *testing.T) {
	f := newJWKSFixture(t)
	url, err := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	v := OAuthVerifier(f.issuer, "https://analytics.example.com/mcp",
		NewJWKSCache(url, f.server.Client()))

	info, err := v(context.Background(), f.sign(t, f.claims(nil)), nil)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if info.UserID != "user@example.com" {
		t.Errorf("UserID = %q", info.UserID)
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"expired", f.sign(t, f.claims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}))},
		{"future nbf", f.sign(t, f.claims(jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()}))},
		{"wrong aud", f.sign(t, f.claims(jwt.MapClaims{"aud": "https://other.example.com"}))},
		{"wrong iss", f.sign(t, f.claims(jwt.MapClaims{"iss": "https://evil.example.com"}))},
		{"garbage", "not.a.jwt"},
	}
	for _, tc := range bad {
		if _, err := v(context.Background(), tc.tok, nil); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestOAuthVerifierRejectsHMACAndNone(t *testing.T) {
	f := newJWKSFixture(t)
	url, _ := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	v := OAuthVerifier(f.issuer, "https://analytics.example.com/mcp",
		NewJWKSCache(url, f.server.Client()))
	// HMAC token signed with an arbitrary secret; alg allowlist must
	// reject it before any key lookup happens.
	hm := jwt.NewWithClaims(jwt.SigningMethodHS256, f.claims(nil))
	hm.Header["kid"] = f.kid
	s, err := hm.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), s, nil); err == nil {
		t.Fatal("HS256 accepted")
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, f.claims(nil))
	sn, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), sn, nil); err == nil {
		t.Fatal("alg=none accepted")
	}
}

func TestJWKSCacheRefetchesOnUnknownKid(t *testing.T) {
	f := newJWKSFixture(t)
	url, _ := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	cache := NewJWKSCache(url, f.server.Client())
	if _, err := cache.Key("k1"); err != nil {
		t.Fatal(err)
	}
	// rotate: new key id served by the fixture
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.key, f.kid = newKey, "k2"
	cache.minRefetch = 0 // the test may not wait a minute
	if _, err := cache.Key("k2"); err != nil {
		t.Fatalf("rotation not picked up: %v", err)
	}
}

func TestStaticVerifier(t *testing.T) {
	v := StaticVerifier("ar_secret")
	if info, err := v(context.Background(), "ar_secret", nil); err != nil || info.UserID != "mcp" {
		t.Fatalf("valid: %v %+v", err, info)
	}
	for _, bad := range []string{"", "ar_", "ar_secre", "ar_secretX", "wrong"} {
		if _, err := v(context.Background(), bad, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCloudflareVerifier(t *testing.T) {
	f := newJWKSFixture(t)
	cache := NewJWKSCache(f.issuer+"/jwks", f.server.Client())
	v := CloudflareVerifier(f.issuer, "aud-tag-1", cache)

	tok := f.sign(t, jwt.MapClaims{
		"iss": f.issuer, "aud": "aud-tag-1", "sub": "user@example.com",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", tok)
	// the opaque bearer is ignored entirely
	info, err := v(context.Background(), "oauth:opaque-ignored", req)
	if err != nil {
		t.Fatalf("valid assertion rejected: %v", err)
	}
	if info.UserID != "user@example.com" {
		t.Errorf("UserID = %q", info.UserID)
	}
	// missing header
	if _, err := v(context.Background(), "x", httptest.NewRequest("POST", "/mcp", nil)); err == nil {
		t.Fatal("missing assertion accepted")
	}
	// wrong aud tag
	wrong := f.sign(t, jwt.MapClaims{"iss": f.issuer, "aud": "other-tag",
		"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	req2 := httptest.NewRequest("POST", "/mcp", nil)
	req2.Header.Set("Cf-Access-Jwt-Assertion", wrong)
	if _, err := v(context.Background(), "x", req2); err == nil {
		t.Fatal("wrong aud accepted")
	}
}
