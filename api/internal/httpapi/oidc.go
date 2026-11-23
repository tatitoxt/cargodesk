package httpapi

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/store"
)

// oidc.go: sign-in with an OIDC ID token (Authorization Code + PKCE on the
// SPA side). The server verifies the token against the issuer's JWKS —
// signature, iss, aud and exp — then maps the email claim to a seeded user
// and issues the same session cookie as the demo login. The SPA has no OIDC
// button yet; a client must obtain the ID token itself.
// Enabled when OIDCIssuer and OIDCClientID are configured; the endpoint 404s
// otherwise, and users must still exist (admin provisioning keeps control of
// organization membership and roles).

const (
	jwksTTL        = time.Hour
	jwksMinRefresh = time.Minute
)

type OIDCVerifier struct {
	issuer, clientID string
	client           *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewOIDCVerifier(issuer, clientID string) *OIDCVerifier {
	return &OIDCVerifier{
		issuer:   issuer,
		clientID: clientID,
		client:   &http.Client{Timeout: 10 * time.Second},
		keys:     map[string]*rsa.PublicKey{},
	}
}

type idTokenClaims struct {
	Iss           string `json:"iss"`
	Aud           any    `json:"aud"` // string or []string
	Sub           string `json:"sub"`
	Exp           int64  `json:"exp"`
	Iat           int64  `json:"iat"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // bool, or "true" for some issuers
}

func (v *OIDCVerifier) email(ctx context.Context, idToken string, now time.Time) (string, error) {
	parts := splitJWT(idToken)
	if parts == nil {
		return "", errors.New("malformed token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(parts[0], &header); err != nil || header.Alg != "RS256" {
		return "", errors.New("unsupported token header")
	}
	var claims idTokenClaims
	if err := json.Unmarshal(parts[1], &claims); err != nil {
		return "", errors.New("malformed claims")
	}

	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return "", err
	}
	if err := verifyRS256(key, idToken); err != nil {
		return "", err
	}

	if claims.Iss != v.issuer {
		return "", fmt.Errorf("issuer %q not accepted", claims.Iss)
	}
	if !audContains(claims.Aud, v.clientID) {
		return "", errors.New("audience mismatch")
	}
	if claims.Exp == 0 || now.Unix() >= claims.Exp {
		return "", errors.New("token expired")
	}
	if claims.Email == "" {
		return "", errors.New("token has no email claim")
	}
	if v, ok := claims.EmailVerified.(bool); !ok || !v {
		if sv, _ := claims.EmailVerified.(string); sv != "true" {
			return "", errors.New("email is not verified by the issuer")
		}
	}
	return claims.Email, nil
}

// key returns the RSA key for kid. The JWKS is refetched when the cache is
// older than jwksTTL, or when kid is unknown and the last fetch is older than
// jwksMinRefresh, so unknown key ids cannot force a fetch per request.
func (v *OIDCVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := time.Since(v.fetchedAt)
	if v.fetchedAt.IsZero() || age > jwksTTL || (v.keys[kid] == nil && age > jwksMinRefresh) {
		if err := v.fetchJWKS(ctx); err != nil {
			return nil, err
		}
	}
	if key := v.keys[kid]; key != nil {
		return key, nil
	}
	return nil, errors.New("unknown key id")
}

func (v *OIDCVerifier) fetchJWKS(ctx context.Context) error {
	var cfg struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.getJSON(ctx, v.issuer+"/.well-known/openid-configuration", &cfg); err != nil {
		return fmt.Errorf("openid configuration: %w", err)
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := v.getJSON(ctx, cfg.JWKSURI, &jwks); err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		exp := 0
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	}
	v.keys, v.fetchedAt = keys, time.Now()
	return nil
}

func (v *OIDCVerifier) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func splitJWT(token string) [][]byte {
	var segs []string
	rest := token
	for range 3 {
		i := indexByte(rest, '.')
		if i < 0 {
			segs = append(segs, rest)
			break
		}
		segs = append(segs, rest[:i])
		rest = rest[i+1:]
	}
	if len(segs) != 3 {
		return nil
	}
	out := make([][]byte, 3)
	for i, seg := range segs {
		b, err := base64.RawURLEncoding.DecodeString(seg)
		if err != nil {
			return nil
		}
		out[i] = b
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func verifyRS256(key *rsa.PublicKey, token string) error {
	i := lastIndexByte(token, '.')
	if i < 0 {
		return errors.New("malformed token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(token[i+1:])
	if err != nil {
		return errors.New("malformed signature")
	}
	h := sha256.Sum256([]byte(token[:i]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig); err != nil {
		return errors.New("invalid signature")
	}
	return nil
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func audContains(aud any, clientID string) bool {
	switch a := aud.(type) {
	case string:
		return a == clientID
	case []any:
		for _, v := range a {
			if v == clientID {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDCVerifier == nil {
		writeError(w, r, http.StatusNotFound, "oidc_disabled", "OIDC sign-in is not configured", nil)
		return
	}
	var in struct {
		IDToken string `json:"id_token"`
	}
	if !decode(w, r, &in) || in.IDToken == "" {
		writeError(w, r, http.StatusBadRequest, "bad_request", "id_token is required", nil)
		return
	}
	email, err := s.cfg.OIDCVerifier.email(r.Context(), in.IDToken, s.now())
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "could not verify the identity token", nil)
		return
	}
	userID, err := s.cfg.Store.UserIDByEmail(r.Context(), email)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "unknown_user",
			"no account for this email; ask an admin to provision you", nil)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.issueSession(w, r, userID)
}
