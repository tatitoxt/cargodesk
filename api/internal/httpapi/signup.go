package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/store"
)

// signupLimiter is a per-IP sliding-window counter on the public registration
// endpoint — organization creation is unauthenticated, so it is the only place
// a bulk-write needs a throttle without a session.
type signupLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

func newSignupLimiter() *signupLimiter { return newLimiter(10, time.Hour) }

func newLimiter(max int, window time.Duration) *signupLimiter {
	return &signupLimiter{hits: map[string][]time.Time{}, max: max, window: window}
}

func (l *signupLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}

// clientIP is the TCP peer, or — when TrustProxy is set because the API only
// sits behind the Next.js proxy — the last X-Forwarded-For hop it appended.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleSignup provisions a new organization plus its first admin account and
// signs the admin in — the self-service path that used to require the seed.
// Public and per-IP rate limited.
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SignupEnabled {
		writeError(w, r, http.StatusNotFound, "signup_disabled", "self-service sign-up is not enabled on this server", nil)
		return
	}
	if !s.signups.allow(s.clientIP(r), s.now()) {
		writeError(w, r, http.StatusTooManyRequests, "too_many_requests", "too many sign-ups from this address — try later", nil)
		return
	}
	var in struct {
		Slug      string `json:"slug"`
		Name      string `json:"name"`
		Timezone  string `json:"timezone"`
		Prefix    string `json:"reference_prefix"`
		Email     string `json:"email"`
		AdminName string `json:"display_name"`
		Password  string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	userID, err := s.cfg.Store.Signup(r.Context(), store.SignupInput{
		Slug: in.Slug, Name: in.Name, Timezone: in.Timezone, Prefix: in.Prefix,
		Email: in.Email, AdminName: in.AdminName, Password: in.Password,
	})
	switch {
	case errors.Is(err, store.ErrSlugTaken):
		writeError(w, r, http.StatusConflict, "slug_taken", err.Error(), nil)
	case errors.Is(err, store.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, "email_taken", err.Error(), nil)
	case err != nil:
		s.writeDomainError(w, r, err)
	default:
		s.issueSession(w, r, userID)
	}
}
