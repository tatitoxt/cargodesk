// Package httpapi exposes the CargoDesk JSON API (routes in routes()).
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

const (
	SessionCookie = "cargodesk_session"
	sessionTTL    = 12 * time.Hour
	// MaxBodyBytes caps every request body.
	MaxBodyBytes = 64 << 10
)

type Config struct {
	Store        *store.Store
	Logger       *slog.Logger
	Clock        func() time.Time
	DemoAuth     bool
	SecureCookie bool
	// AttachmentsDir is the blob root for shipment attachments; empty disables
	// upload/download endpoints. MaxAttachmentBytes caps a single file (default 10 MiB).
	AttachmentsDir     string
	MaxAttachmentBytes int64
	// OIDCVerifier validates ID tokens for POST /api/v1/auth/oidc; nil disables it.
	OIDCVerifier *OIDCVerifier
	// EmailEnabled reports whether SMTP env config is present (SMTP_HOST); the
	// settings UI uses it to explain pending email deliveries.
	EmailEnabled bool
	// SignupEnabled exposes public POST /api/v1/signup (off by default).
	SignupEnabled bool
	// TrustProxy uses X-Forwarded-For for rate-limit keys (API behind the web proxy).
	TrustProxy bool
	// AllowCustomWhatsAppAPI permits api_url hosts other than graph.facebook.com.
	AllowCustomWhatsAppAPI bool
}

type Server struct {
	cfg     Config
	mux     *http.ServeMux
	signups *signupLimiter
	logins  *signupLimiter
}

func New(cfg Config) *Server {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), signups: newSignupLimiter(), logins: newLimiter(10, 15*time.Minute)}
	s.routes()
	return s
}

func (s *Server) now() time.Time { return s.cfg.Clock().UTC() }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.handleHealth)
	m.HandleFunc("GET /api/v1/auth/demo-users", s.handleDemoUsers)
	m.HandleFunc("POST /api/v1/auth/demo-login", s.handleDemoLogin)
	m.HandleFunc("POST /api/v1/auth/oidc", s.handleOIDCLogin)
	m.HandleFunc("POST /api/v1/auth/password-login", s.handlePasswordLogin)
	m.HandleFunc("POST /api/v1/signup", s.handleSignup)
	m.HandleFunc("POST /api/v1/users/invite", s.authed(s.handleInvite))
	m.HandleFunc("POST /api/v1/me/password", s.authed(s.handleChangePassword))
	m.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/v1/me", s.authed(s.handleMe))
	m.HandleFunc("GET /api/v1/users", s.authed(s.handleUsers))
	m.HandleFunc("POST /api/v1/users", s.authed(s.handleCreateUser))
	m.HandleFunc("PATCH /api/v1/users/{id}", s.authed(s.handleUpdateUser))
	m.HandleFunc("DELETE /api/v1/users/{id}", s.authed(s.handleDeleteUser))
	m.HandleFunc("GET /api/v1/shipments", s.authed(s.handleList))
	m.HandleFunc("POST /api/v1/shipments", s.editor(s.idempotent("create", s.handleCreate)))
	m.HandleFunc("GET /api/v1/shipments/{id}", s.authed(s.handleGet))
	m.HandleFunc("GET /api/v1/shipments/{id}/events", s.authed(s.handleEvents))
	m.HandleFunc("POST /api/v1/shipments/{id}/track-link", s.editor(s.handleRotateTrackLink))
	m.HandleFunc("GET /api/v1/track/{token}", s.handleTrack)
	m.HandleFunc("POST /api/v1/shipments/{id}/transitions", s.editor(s.idempotent("transition", s.handleTransition)))
	m.HandleFunc("POST /api/v1/shipments/{id}/events", s.editor(s.handleAddEvent))
	m.HandleFunc("GET /api/v1/shipments/{id}/attachments", s.authed(s.handleListAttachments))
	m.HandleFunc("POST /api/v1/shipments/{id}/attachments", s.editor(s.handleUploadAttachment))
	m.HandleFunc("GET /api/v1/shipments/{id}/attachments/{aid}", s.authed(s.handleDownloadAttachment))
	m.HandleFunc("DELETE /api/v1/shipments/{id}/attachments/{aid}", s.editor(s.handleDeleteAttachment))
	m.HandleFunc("PATCH /api/v1/shipments/{id}/assignment", s.editor(s.handleAssign))
	m.HandleFunc("GET /api/v1/exports/shipments.csv", s.authed(s.handleExport))
	m.HandleFunc("GET /api/v1/views", s.authed(s.handleListViews))
	m.HandleFunc("POST /api/v1/views", s.authed(s.handleCreateView))
	m.HandleFunc("DELETE /api/v1/views/{id}", s.authed(s.handleDeleteView))
	m.HandleFunc("GET /api/v1/settings/sla", s.authed(s.handleGetSLA))
	m.HandleFunc("PUT /api/v1/settings/sla", s.authed(s.handlePutSLA))
	m.HandleFunc("GET /api/v1/settings/business-hours", s.authed(s.handleGetBusinessHours))
	m.HandleFunc("PUT /api/v1/settings/business-hours", s.authed(s.handlePutBusinessHours))
	m.HandleFunc("DELETE /api/v1/settings/business-hours", s.authed(s.handleDeleteBusinessHours))
	m.HandleFunc("GET /api/v1/settings/retention", s.authed(s.handleGetRetention))
	m.HandleFunc("PUT /api/v1/settings/retention", s.authed(s.handlePutRetention))
	m.HandleFunc("POST /api/v1/settings/retention/purge", s.authed(s.handlePurgeRetention))
	m.HandleFunc("GET /api/v1/settings/webhook", s.authed(s.handleGetWebhook))
	m.HandleFunc("PUT /api/v1/settings/webhook", s.authed(s.handlePutWebhook))
	m.HandleFunc("DELETE /api/v1/settings/webhook", s.authed(s.handleDeleteWebhook))
	m.HandleFunc("POST /api/v1/settings/webhook/test", s.authed(s.handleTestWebhook))
	m.HandleFunc("GET /api/v1/settings/slack", s.authed(s.handleGetSlack))
	m.HandleFunc("PUT /api/v1/settings/slack", s.authed(s.handlePutSlack))
	m.HandleFunc("DELETE /api/v1/settings/slack", s.authed(s.handleDeleteSlack))
	m.HandleFunc("GET /api/v1/settings/whatsapp", s.authed(s.handleGetWhatsApp))
	m.HandleFunc("PUT /api/v1/settings/whatsapp", s.authed(s.handlePutWhatsApp))
	m.HandleFunc("DELETE /api/v1/settings/whatsapp", s.authed(s.handleDeleteWhatsApp))
	m.HandleFunc("GET /api/v1/settings/email", s.authed(s.handleGetEmail))
	m.HandleFunc("PUT /api/v1/settings/email", s.authed(s.handlePutEmail))
}

// ServeHTTP wraps the mux with recovery, correlation IDs, body limits,
// security headers and structured access logs.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	corr := r.Header.Get("X-Correlation-ID")
	if !corrRe.MatchString(corr) {
		corr = newID()
	}
	info := &reqInfo{correlationID: corr}
	r = r.WithContext(context.WithValue(r.Context(), reqKey{}, info))
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	h := sw.Header()
	h.Set("X-Correlation-ID", corr)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(sw, r.Body, MaxBodyBytes)
	defer func() {
		if rec := recover(); rec != nil {
			s.cfg.Logger.Error("panic", "correlation_id", corr, "panic", rec)
			writeError(sw, r, http.StatusInternalServerError, "internal_error", "internal error", nil)
		}
		s.cfg.Logger.Info("request", "method", r.Method, "path", logPath(r.URL.Path), "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "correlation_id", corr, "user_id", info.userID)
	}()
	s.mux.ServeHTTP(sw, r)
}

// logPath keeps bearer-like path segments (public tracking tokens) out of logs.
func logPath(p string) string {
	if strings.HasPrefix(p, "/api/v1/track/") {
		return "/api/v1/track/[redacted]"
	}
	return p
}

var corrRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type reqKey struct{}

type reqInfo struct {
	correlationID string
	userID        string
	actor         store.Actor
}

func info(r *http.Request) *reqInfo {
	if i, ok := r.Context().Value(reqKey{}).(*reqInfo); ok {
		return i
	}
	return &reqInfo{}
}

func correlationID(r *http.Request) string { return info(r).correlationID }

func actor(r *http.Request) store.Actor { return info(r).actor }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "sign in required", nil)
			return
		}
		a, err := s.cfg.Store.ActorBySession(r.Context(), c.Value, s.now())
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "session expired or invalid", nil)
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		i := info(r)
		i.actor, i.userID = a, a.UserID
		if a.MustChangePassword && !passwordChangeAllowed(r) {
			writeError(w, r, http.StatusForbidden, "password_change_required",
				"set a new password before using the API", nil)
			return
		}
		next(w, r)
	}
}

// passwordChangeAllowed lists the routes a member on a temporary password may
// call: reading their profile and replacing the password.
func passwordChangeAllowed(r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/me/password":
		return true
	}
	return false
}

// editor requires an authenticated coordinator or admin.
func (s *Server) editor(next http.HandlerFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		if !actor(r).Role.CanEdit() {
			writeError(w, r, http.StatusForbidden, "forbidden", "your role cannot change shipments", nil)
			return
		}
		next(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a stable error shape; extra fields are merged in.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string, extra map[string]any) {
	body := map[string]any{"error": code, "message": msg, "correlation_id": correlationID(r)}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.cfg.Logger.Error("internal error", "correlation_id", correlationID(r), "error", err.Error())
	writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error", nil)
}

// decode reads a JSON body, enforcing the content type (a CSRF guard: cross
// site forms cannot send application/json) and the size limit.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 64 KiB", nil)
			return false
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body is not valid JSON: "+err.Error(), nil)
		return false
	}
	return true
}

// writeDomainError maps store/domain/workflow errors to HTTP responses.
func (s *Server) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		ve  *domain.ValidationError
		vc  *store.VersionConflictError
		ite *workflow.InvalidTransitionError
		re  *workflow.RequirementsError
	)
	switch {
	case errors.As(err, &ve):
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed", ve.Error(), map[string]any{"fields": ve.Fields})
	case errors.As(err, &vc):
		writeError(w, r, http.StatusConflict, "version_conflict",
			"this shipment was changed by someone else; reload to see the current version",
			map[string]any{"version_current": vc.Current})
	case errors.As(err, &ite):
		writeError(w, r, http.StatusConflict, "invalid_transition", ite.Error(),
			map[string]any{"current_state": ite.From, "requested_state": ite.To, "allowed": ite.Allowed})
	case errors.As(err, &re):
		writeError(w, r, http.StatusUnprocessableEntity, "transition_requirements", re.Error(),
			map[string]any{"current_state": re.From, "requested_state": re.To, "fields": re.Fields})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found", nil)
	case errors.Is(err, store.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, "email_taken", "a user with this email already exists", nil)
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, r, http.StatusConflict, "last_admin",
			"the organization must keep at least one admin", nil)
	case errors.Is(err, store.ErrUserHasHistory):
		writeError(w, r, http.StatusConflict, "user_has_history", err.Error(), nil)
	case errors.Is(err, store.ErrClosed):
		writeError(w, r, http.StatusConflict, "shipment_closed", "shipment is in a terminal state", nil)
	default:
		s.internal(w, r, err)
	}
}
