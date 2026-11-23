package httpapi_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/httpapi"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/testdb"
)

// fakeIssuer serves an OpenID configuration and JWKS for a generated RSA key.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": f.srv.URL + "/jwks.json"})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "test", "n": n, "e": e},
		}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) sign(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	h := sha256.Sum256([]byte(header + "." + body))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, h[:])
	return header + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func oidcEnv(t *testing.T, issuer, clientID string) *env {
	pool := testdb.Fresh(t)
	e := &env{t: t, pool: pool}
	now := testdb.Now
	e.now.Store(&now)
	h := httpapi.New(httpapi.Config{Store: store.New(pool),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:        func() time.Time { return *e.now.Load() },
		OIDCVerifier: httpapi.NewOIDCVerifier(issuer, clientID),
	})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func TestOIDCLogin(t *testing.T) {
	issuer := newFakeIssuer(t)
	e := oidcEnv(t, issuer.srv.URL, "cargodesk-web")
	c := e.anon()

	valid := issuer.sign(map[string]any{
		"iss": issuer.srv.URL, "aud": "cargodesk-web", "sub": "ext-1",
		"email": "ana.coordinator@cargodesk.example", "email_verified": true,
		"exp": e.now.Load().Add(time.Hour).Unix(), "iat": e.now.Load().Unix(),
	})
	res := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": valid}, nil)
	if res.status != 200 {
		t.Fatalf("oidc login: %d %s", res.status, res.raw)
	}
	if me := c.do("GET", "/api/v1/me", nil, nil); me.status != 200 {
		t.Fatalf("session cookie not issued: %d", me.status)
	}

	// tampered signature -> 401
	bad := valid[:len(valid)-2] + "xx"
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": bad}, nil); r.status != 401 {
		t.Fatalf("tampered token: %d", r.status)
	}

	// wrong audience -> 401
	wrongAud := issuer.sign(map[string]any{
		"iss": issuer.srv.URL, "aud": "someone-else", "sub": "ext-1",
		"email": "ana.coordinator@cargodesk.example", "email_verified": true, "exp": e.now.Load().Add(time.Hour).Unix(),
	})
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": wrongAud}, nil); r.status != 401 {
		t.Fatalf("wrong audience: %d", r.status)
	}

	// expired -> 401
	expired := issuer.sign(map[string]any{
		"iss": issuer.srv.URL, "aud": "cargodesk-web", "sub": "ext-1",
		"email": "ana.coordinator@cargodesk.example", "email_verified": true, "exp": e.now.Load().Add(-time.Hour).Unix(),
	})
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": expired}, nil); r.status != 401 {
		t.Fatalf("expired token: %d", r.status)
	}

	// verified but unknown email -> 403 (admin provisioning required)
	stranger := issuer.sign(map[string]any{
		"iss": issuer.srv.URL, "aud": "cargodesk-web", "sub": "ext-2",
		"email": "stranger@nowhere.example", "email_verified": true, "exp": e.now.Load().Add(time.Hour).Unix(),
	})
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": stranger}, nil); r.status != 403 {
		t.Fatalf("unknown user: %d", r.status)
	}

	// unverified email -> 401, even for a provisioned user
	unverified := issuer.sign(map[string]any{
		"iss": issuer.srv.URL, "aud": "cargodesk-web", "sub": "ext-3",
		"email": "ana.coordinator@cargodesk.example", "email_verified": false, "exp": e.now.Load().Add(time.Hour).Unix(),
	})
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": unverified}, nil); r.status != 401 {
		t.Fatalf("unverified email: %d", r.status)
	}

	// self-registered accounts are never matched by email
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET self_registered = true WHERE email = 'ana.coordinator@cargodesk.example'`); err != nil {
		t.Fatal(err)
	}
	if r := c.do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": valid}, nil); r.status != 403 {
		t.Fatalf("self-registered account matched: %d", r.status)
	}
}

func TestOIDCDisabled(t *testing.T) {
	e := newEnv(t, true)
	r := e.anon().do("POST", "/api/v1/auth/oidc", map[string]string{"id_token": "x"}, nil)
	if r.status != 404 {
		t.Fatalf("oidc endpoint should 404 when unconfigured: %d", r.status)
	}
}
