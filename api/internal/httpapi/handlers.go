package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/csvexport"
	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.Ping(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "db_unavailable", "database unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDemoUsers(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DemoAuth {
		writeError(w, r, http.StatusNotFound, "demo_auth_disabled", "demo sign-in is disabled", nil)
		return
	}
	users, err := s.cfg.Store.LoginUsers(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleDemoLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DemoAuth {
		writeError(w, r, http.StatusNotFound, "demo_auth_disabled", "demo sign-in is disabled", nil)
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !domain.IsUUID(in.UserID) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed", "user_id is required",
			map[string]any{"fields": []domain.FieldError{{Field: "user_id", Message: "must be a user ID"}}})
		return
	}
	token, err := s.cfg.Store.CreateSession(r.Context(), in.UserID, s.now(), sessionTTL)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "unknown user", nil)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookie, MaxAge: int(sessionTTL.Seconds())})
	a, err := s.cfg.Store.ActorBySession(r.Context(), token, s.now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.cfg.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, actor(r))
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.cfg.Store.Users(r.Context(), actor(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func parseFilter(r *http.Request, a store.Actor) (store.ListFilter, error) {
	q := r.URL.Query()
	f := store.ListFilter{Query: q.Get("q")}
	for _, raw := range q["state"] {
		for _, st := range strings.Split(raw, ",") {
			if st = strings.TrimSpace(st); st == "" {
				continue
			}
			if !workflow.State(st).Valid() {
				return f, &domain.ValidationError{Fields: []domain.FieldError{{Field: "state", Message: "unknown state " + strconv.Quote(st)}}}
			}
			f.States = append(f.States, workflow.State(st))
		}
	}
	switch owner := q.Get("owner"); owner {
	case "me":
		f.Owner = a.UserID
	default:
		f.Owner = owner
	}
	var err error
	if f.Overdue, err = parseBool(q.Get("overdue")); err != nil {
		return f, &domain.ValidationError{Fields: []domain.FieldError{{Field: "overdue", Message: "must be true or false"}}}
	}
	switch d := q.Get("due"); d {
	case "":
	case "today":
		f.DueToday = true
	default:
		return f, &domain.ValidationError{Fields: []domain.FieldError{{Field: "due", Message: "must be \"today\""}}}
	}
	return f, nil
}

func parseBool(s string) (bool, error) {
	if s == "" {
		return false, nil
	}
	return strconv.ParseBool(s)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	f, err := parseFilter(r, a)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	now := s.now()
	items, err := s.cfg.Store.ListShipments(r.Context(), a, f, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(items), "now": now, "timezone": a.Org.Timezone,
	})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateShipmentInput
	if !decode(w, r, &in) {
		return
	}
	in.Normalize()
	a, now := actor(r), s.now()
	if err := in.Validate(now, a.Org.Loc); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	sh, err := s.cfg.Store.CreateShipment(r.Context(), a, in, now, correlationID(r))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/shipments/"+sh.ID)
	writeJSON(w, http.StatusCreated, sh)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.cfg.Store.GetShipment(r.Context(), actor(r), r.PathValue("id"), s.now())
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.cfg.Store.Events(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

type transitionBody struct {
	Version int `json:"version"`
	workflow.TransitionInput
}

func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	var in transitionBody
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 {
		s.writeDomainError(w, r, &domain.ValidationError{Fields: []domain.FieldError{{Field: "version", Message: "is required"}}})
		return
	}
	sh, err := s.cfg.Store.Transition(r.Context(), actor(r), r.PathValue("id"), in.Version, in.TransitionInput, s.now(), correlationID(r))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	var in domain.AssignmentInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	sh, err := s.cfg.Store.Assign(r.Context(), actor(r), r.PathValue("id"), in, s.now(), correlationID(r))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

func (s *Server) handleAddEvent(w http.ResponseWriter, r *http.Request) {
	var in domain.EventInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	a, id, now, corr := actor(r), r.PathValue("id"), s.now(), correlationID(r)
	switch in.Kind {
	case domain.EventNote:
		ev, err := s.cfg.Store.AddNote(r.Context(), a, id, in.Body, now, corr)
		if err != nil {
			s.writeDomainError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"event": ev})
	case domain.EventException:
		sh, err := s.cfg.Store.OpenException(r.Context(), a, id, in, now, corr)
		if err != nil {
			s.writeDomainError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"shipment": sh})
	case domain.EventExceptionResolved:
		ev, err := s.cfg.Store.ResolveException(r.Context(), a, id, in, now, corr)
		if err != nil {
			s.writeDomainError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"event": ev})
	}
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// handleExport streams the allow-listed CSV. Same filters as the list.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanExport() {
		writeError(w, r, http.StatusForbidden, "forbidden", "your role cannot export data", nil)
		return
	}
	f, err := parseFilter(r, a)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	f.Limit = 500
	items, err := s.cfg.Store.ListShipments(r.Context(), a, f, s.now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shipments-%s.csv"`, s.now().Format("20060102")))
	cw, err := csvexport.NewWriter(w)
	if err != nil {
		return
	}
	for _, sh := range items {
		owner := ""
		if sh.Owner != nil {
			owner = sh.Owner.DisplayName
		}
		_ = cw.Write(map[string]string{
			"reference": sh.Reference, "state": string(sh.State), "priority": string(sh.Priority), "owner": owner,
			"account_name": sh.AccountName, "origin": sh.Origin, "destination": sh.Destination, "cargo_type": sh.CargoType,
			"required_date": sh.RequiredDate, "next_action_at": fmtTime(sh.NextActionAt), "due_at": fmtTime(&sh.DueAt),
			"created_at": fmtTime(&sh.CreatedAt),
		})
	}
	if err := cw.Flush(); err != nil {
		s.cfg.Logger.Error("csv export", "correlation_id", correlationID(r), "error", err.Error())
	}
}

func (s *Server) handleGetSLA(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	p, err := s.cfg.Store.SLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": p, "timezone": a.Org.Timezone})
}

func (s *Server) handlePutSLA(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the SLA policy", nil)
		return
	}
	var in struct {
		Hours sla.Policy `json:"hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.cfg.Store.SetSLAPolicy(r.Context(), a, in.Hours); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": in.Hours, "timezone": a.Org.Timezone})
}

// idempotent replays the stored response when a request repeats an
// Idempotency-Key, and rejects reuse of a key with a different body.
func (s *Server) idempotent(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r)
			return
		}
		if !corrRe.MatchString(key) {
			writeError(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 1-100 URL-safe characters", nil)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 64 KiB", nil)
			return
		}
		a := actor(r)
		fullScope := scope + " " + r.URL.Path
		hash := store.RequestHash(fullScope, body)
		prev, err := s.cfg.Store.ReserveIdempotent(r.Context(), a, key, fullScope, hash, s.now())
		switch {
		case errors.Is(err, store.ErrIdempotencyMismatch):
			writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", err.Error(), nil)
			return
		case errors.Is(err, store.ErrIdempotencyInProgress):
			writeError(w, r, http.StatusConflict, "idempotency_in_progress", err.Error(), nil)
			return
		case err != nil:
			s.internal(w, r, err)
			return
		case prev != nil:
			w.Header().Set("Idempotent-Replayed", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(prev.Status)
			_, _ = w.Write(prev.Body)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		// Only successes are replayed; failed requests free the key so a
		// corrected retry with the same key can run.
		ctx := context.WithoutCancel(r.Context())
		if rec.status >= 200 && rec.status < 300 {
			err = s.cfg.Store.CompleteIdempotent(ctx, a, key, rec.status, rec.buf.Bytes())
		} else {
			err = s.cfg.Store.ReleaseIdempotent(ctx, a, key)
		}
		if err != nil {
			s.cfg.Logger.Error("finish idempotency key", "correlation_id", correlationID(r), "error", err.Error())
		}
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}
