package mcpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// allowedAlgs is the asymmetric allowlist (endpoint spec §5.2): never
// `none`, never HMAC — a JWKS public key must not be usable as an HMAC
// secret.
var allowedAlgs = []string{
	"RS256", "RS384", "RS512",
	"ES256", "ES384", "ES512",
	"PS256", "PS384", "PS512",
}

// StaticVerifier implements MCP_AUTH_MODE=token: constant-time compare
// against the single MCP_TOKEN. The middleware wrapping it must allow
// missing expiration (a static token has no exp).
func StaticVerifier(token string) auth.TokenVerifier {
	want := []byte(token)
	return func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare(want, []byte(got)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: "mcp"}, nil
	}
}

// DiscoverJWKSURL resolves the issuer's RFC 8414 metadata to its
// jwks_uri. Called once at startup so a typo'd issuer fails the boot,
// not the first request.
func DiscoverJWKSURL(ctx context.Context, issuer string, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		issuer+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mcpserver: issuer metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcpserver: issuer metadata: HTTP %d", resp.StatusCode)
	}
	var meta struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("mcpserver: issuer metadata: %w", err)
	}
	if meta.JWKSURI == "" {
		return "", fmt.Errorf("mcpserver: issuer metadata has no jwks_uri")
	}
	return meta.JWKSURI, nil
}

// JWKSCache fetches and caches an issuer's keys, refetching when an
// unknown kid appears (key rotation) but at most once per minRefetch.
type JWKSCache struct {
	url    string
	client *http.Client

	mu         sync.Mutex
	keys       map[string]any // kid -> *rsa.PublicKey | *ecdsa.PublicKey
	lastFetch  time.Time
	minRefetch time.Duration
}

func NewJWKSCache(jwksURL string, client *http.Client) *JWKSCache {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKSCache{url: jwksURL, client: client, minRefetch: time.Minute}
}

func (c *JWKSCache) Key(kid string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	if time.Since(c.lastFetch) < c.minRefetch {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	if err := c.fetchLocked(); err != nil {
		return nil, err
	}
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

func (c *JWKSCache) fetchLocked() error {
	resp, err := c.client.Get(c.url)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty, Kid, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("jwks parse: %w", err)
	}
	keys := map[string]any{}
	for _, k := range doc.Keys {
		switch k.Kty {
		case "RSA":
			n, err := base64.RawURLEncoding.DecodeString(k.N)
			if err != nil {
				continue
			}
			e, err := base64.RawURLEncoding.DecodeString(k.E)
			if err != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(n),
				E: int(new(big.Int).SetBytes(e).Int64()),
			}
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			case "P-521":
				curve = elliptic.P521()
			default:
				continue
			}
			x, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil {
				continue
			}
			y, err := base64.RawURLEncoding.DecodeString(k.Y)
			if err != nil {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{Curve: curve,
				X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		}
	}
	c.keys = keys
	c.lastFetch = time.Now()
	return nil
}

func (c *JWKSCache) keyfunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	return c.Key(kid)
}

// verifyJWT is the shared body of the oauth and cloudflare verifiers.
func verifyJWT(raw, issuer, audience string, cache *JWKSCache) (*auth.TokenInfo, error) {
	tok, err := jwt.Parse(raw, cache.keyfunc,
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	sub, _ := claims.GetSubject()
	exp, _ := claims.GetExpirationTime()
	info := &auth.TokenInfo{UserID: sub}
	if exp != nil {
		info.Expiration = exp.Time
	}
	if scope, ok := claims["scope"].(string); ok && scope != "" {
		info.Scopes = strings.Fields(scope)
	}
	return info, nil
}

// OAuthVerifier implements MCP_AUTH_MODE=oauth: the bearer token itself
// is a JWT from the external IdP.
func OAuthVerifier(issuer, audience string, cache *JWKSCache) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		return verifyJWT(token, issuer, audience, cache)
	}
}

// CloudflareVerifier implements MCP_AUTH_MODE=cloudflare (endpoint spec
// §5.2): under Access managed OAuth the bearer is opaque and validated
// at the edge; the origin validates the resolved identity JWT in
// Cf-Access-Jwt-Assertion. This is also what closes the
// direct-to-origin bypass — no valid assertion, no access.
//
// teamDomain is normally a bare Cloudflare Access team domain
// (e.g. "myteam.cloudflareaccess.com"), which is prefixed with
// "https://" to form the issuer. Tests (and any caller that already
// has a full issuer URL, e.g. an httptest server) may pass a value
// that already carries a scheme; it is used as-is.
func CloudflareVerifier(teamDomain, aud string, cache *JWKSCache) auth.TokenVerifier {
	issuer := teamDomain
	if !strings.HasPrefix(issuer, "http://") && !strings.HasPrefix(issuer, "https://") {
		issuer = "https://" + issuer
	}
	return func(_ context.Context, _ string, req *http.Request) (*auth.TokenInfo, error) {
		if req == nil {
			return nil, auth.ErrInvalidToken
		}
		assertion := req.Header.Get("Cf-Access-Jwt-Assertion")
		if assertion == "" {
			return nil, fmt.Errorf("%w: no Cf-Access-Jwt-Assertion header", auth.ErrInvalidToken)
		}
		return verifyJWT(assertion, issuer, aud, cache)
	}
}
