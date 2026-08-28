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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type jwksFixture struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
	issuer string

	fetches int64 // count of /jwks handler invocations
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
		atomic.AddInt64(&f.fetches, 1)
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

// TestJWKSCacheThrottlesRefetch proves the default minRefetch window
// suppresses a flood of fetches triggered by repeated unknown-kid
// lookups: after the warm-up fetch, hammering Key with an unknown kid
// must not cause more than one additional fetch to the JWKS endpoint.
func TestJWKSCacheThrottlesRefetch(t *testing.T) {
	f := newJWKSFixture(t)
	url, err := DiscoverJWKSURL(context.Background(), f.issuer, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	cache := NewJWKSCache(url, f.server.Client())
	// warm the cache; leave cache.minRefetch at its default (time.Minute).
	if _, err := cache.Key("k1"); err != nil {
		t.Fatal(err)
	}
	warmFetches := atomic.LoadInt64(&f.fetches)
	if warmFetches == 0 {
		t.Fatal("warm-up did not fetch the JWKS endpoint")
	}

	for i := 0; i < 5; i++ {
		_, err := cache.Key("unknown-kid")
		if err == nil {
			t.Fatal("unknown kid accepted")
		}
		if !strings.Contains(err.Error(), "unknown key id") {
			t.Errorf("unexpected error: %v", err)
		}
	}

	if got := atomic.LoadInt64(&f.fetches) - warmFetches; got > 1 {
		t.Errorf("fetch count rose by %d beyond warm-up, want <=1 (throttle not applied)", got)
	}
}

// TestJWKSCacheSkipsMalformedKeys proves a JWKS document containing
// unparseable or unsupported entries doesn't panic and doesn't prevent
// a well-formed key in the same document from being usable.
func TestJWKSCacheSkipsMalformedKeys(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(*rsa.PublicKey)
	validN := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	validE := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "bad-b64", "n": "not valid base64!!!", "e": validE},
			{"kty": "oct", "kid": "unsupported-kty", "k": "irrelevant"},
			{"kty": "RSA", "kid": "good", "n": validN, "e": validE},
		}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cache := NewJWKSCache(server.URL+"/jwks", server.Client())

	got, err := cache.Key("good")
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if _, ok := got.(*rsa.PublicKey); !ok {
		t.Errorf("got %T, want *rsa.PublicKey", got)
	}

	for _, kid := range []string{"bad-b64", "unsupported-kty"} {
		if _, err := cache.Key(kid); err == nil {
			t.Errorf("%s: malformed/unsupported key accepted", kid)
		}
	}
}

func TestDiscoverJWKSURLNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	if _, err := DiscoverJWKSURL(context.Background(), server.URL, server.Client()); err == nil {
		t.Fatal("non-200 issuer metadata response accepted")
	}
}

func TestDiscoverJWKSURLMissingJWKSURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": "https://example.com"})
	}))
	t.Cleanup(server.Close)

	if _, err := DiscoverJWKSURL(context.Background(), server.URL, server.Client()); err == nil {
		t.Fatal("issuer metadata without jwks_uri accepted")
	}
}
