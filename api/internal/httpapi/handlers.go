package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
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
	s.issueSession(w, r, in.UserID)
}

// issueSession creates a session for userID, sets the cookie and answers with
// the resolved actor — shared by the demo and OIDC sign-ins.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID string) {
	s.issueSessionWith(w, r, userID, nil)
}

// issueSessionWith behaves like issueSession but merges extra fields into the
// JSON body (e.g. must_change_password after a password login).
func (s *Server) issueSessionWith(w http.ResponseWriter, r *http.Request, userID string, extra map[string]any) {
	token, err := s.cfg.Store.CreateSession(r.Context(), userID, s.now(), sessionTTL)
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
	if len(extra) == 0 {
		writeJSON(w, http.StatusOK, a)
		return
	}
	var body map[string]any
	raw, err := json.Marshal(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		s.internal(w, r, err)
		return
	}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
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
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 500 {
			return f, &domain.ValidationError{Fields: []domain.FieldError{{Field: "limit", Message: "must be an integer between 1 and 500"}}}
		}
		f.Limit = v
	}
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return f, &domain.ValidationError{Fields: []domain.FieldError{{Field: "offset", Message: "must be a non-negative integer"}}}
		}
		f.Offset = v
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
	items, total, err := s.cfg.Store.ListShipments(r.Context(), a, f, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(items), "total": total,
		"limit": f.Limit, "offset": f.Offset,
		"now": now, "timezone": a.Org.Timezone,
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
	f.Offset = 0 // export always starts from the first row
	items, _, err := s.cfg.Store.ListShipments(r.Context(), a, f, s.now())
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

func (s *Server) handleListViews(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	views, err := s.cfg.Store.ListViews(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

func (s *Server) handleCreateView(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	var in struct {
		Name    string            `json:"name"`
		Filters map[string]string `json:"filters"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.cfg.Store.CreateView(r.Context(), a, in.Name, in.Filters, s.cfg.Clock())
	switch {
	case errors.Is(err, store.ErrViewNameTaken):
		writeError(w, r, http.StatusConflict, "view_name_taken", err.Error(), nil)
	case err != nil:
		s.writeDomainError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) handleDeleteView(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if err := s.cfg.Store.DeleteView(r.Context(), a, r.PathValue("id")); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetSLA(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	p, err := s.cfg.Store.SLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sp, err := s.cfg.Store.ScheduleSLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	tp, err := s.cfg.Store.TransitSLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": p, "schedule_hours": sp, "transit_hours": tp, "timezone": a.Org.Timezone})
}

func (s *Server) handlePutSLA(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the SLA policy", nil)
		return
	}
	var in struct {
		Hours         *sla.Policy `json:"hours"`
		ScheduleHours *sla.Policy `json:"schedule_hours"`
		TransitHours  *sla.Policy `json:"transit_hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Hours == nil && in.ScheduleHours == nil && in.TransitHours == nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed", "hours, schedule_hours or transit_hours is required",
			map[string]any{"fields": []domain.FieldError{{Field: "hours", Message: "at least one policy is required"}}})
		return
	}
	if in.Hours != nil {
		if err := s.cfg.Store.SetSLAPolicy(r.Context(), a, *in.Hours); err != nil {
			s.writeDomainError(w, r, err)
			return
		}
	}
	if in.ScheduleHours != nil {
		if err := s.cfg.Store.SetScheduleSLAPolicy(r.Context(), a, *in.ScheduleHours); err != nil {
			s.writeDomainError(w, r, err)
			return
		}
	}
	if in.TransitHours != nil {
		if err := s.cfg.Store.SetTransitSLAPolicy(r.Context(), a, *in.TransitHours); err != nil {
			s.writeDomainError(w, r, err)
			return
		}
	}
	p, err := s.cfg.Store.SLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sp, err := s.cfg.Store.ScheduleSLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	tp, err := s.cfg.Store.TransitSLAPolicy(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": p, "schedule_hours": sp, "transit_hours": tp, "timezone": a.Org.Timezone})
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
			err = s.cfg.Store.CompleteIdempotent(ctx, a, key, hash, rec.status, rec.buf.Bytes())
		} else {
			err = s.cfg.Store.ReleaseIdempotent(ctx, a, key, hash)
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

func (s *Server) handleGetBusinessHours(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	cal, err := s.cfg.Store.BusinessCalendar(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendar": cal, "timezone": a.Org.Timezone})
}

func (s *Server) handlePutBusinessHours(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change business hours", nil)
		return
	}
	var in struct {
		Calendar sla.Calendar `json:"calendar"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.cfg.Store.SetBusinessCalendar(r.Context(), a, in.Calendar); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendar": in.Calendar, "timezone": a.Org.Timezone})
}

func (s *Server) handleDeleteBusinessHours(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change business hours", nil)
		return
	}
	if err := s.cfg.Store.ClearBusinessCalendar(r.Context(), a); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"attachmentRetentionDays": a.Org.AttachmentRetentionDays,
	})
}

func (s *Server) handlePutRetention(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change retention", nil)
		return
	}
	var in struct {
		AttachmentRetentionDays int `json:"attachmentRetentionDays"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AttachmentRetentionDays < 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_retention",
			"attachmentRetentionDays must be >= 0 (0 keeps attachments forever)", nil)
		return
	}
	if err := s.cfg.Store.SetAttachmentRetention(r.Context(), a, in.AttachmentRetentionDays); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attachmentRetentionDays": in.AttachmentRetentionDays,
	})
}

// handlePurgeRetention runs the retention pass synchronously (admin-only).
// The same store call runs on a timer in production; this endpoint exists so
// operators can force a pass and so the behavior is testable end to end.
func (s *Server) handlePurgeRetention(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can run retention purges", nil)
		return
	}
	n, err := s.PurgeExpiredAttachments(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}

// PurgeExpiredAttachments deletes expired attachment rows and their blobs,
// across every org that configured a retention window. It is used by the
// admin endpoint above and by the background retention loop in main.
func (s *Server) PurgeExpiredAttachments(ctx context.Context) (int, error) {
	purged, err := s.cfg.Store.PurgeExpiredAttachments(ctx, s.now())
	if err != nil {
		return 0, err
	}
	for _, p := range purged {
		if path, perr := s.blobPath(p.StorageKey); perr == nil {
			_ = os.Remove(path) // best-effort; metadata row is already gone
		}
	}
	return len(purged), nil
}

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (s *Server) handleGetEmail(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	emails, err := s.cfg.Store.EmailRecipients(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	deliveries, err := s.cfg.Store.RecentDeliveries(r.Context(), a, 10)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []map[string]any{}
	for _, d := range deliveries {
		if d.Channel == "email" {
			out = append(out, map[string]any{
				"id": d.ID, "action": d.Action, "delivered": d.Delivered,
				"attempts": d.Attempts, "last_error": d.LastError,
				"created_at": d.CreatedAt, "delivered_at": d.DeliveredAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recipients":   emails,
		"smtp_enabled": s.cfg.EmailEnabled,
		"deliveries":   out,
	})
}

func (s *Server) handlePutEmail(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change email notifications", nil)
		return
	}
	var in struct {
		Recipients []string `json:"recipients"`
	}
	if !decode(w, r, &in) {
		return
	}
	for _, em := range in.Recipients {
		if !emailRE.MatchString(em) {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"recipients must be valid email addresses", map[string]any{"recipients": em})
			return
		}
	}
	if err := s.cfg.Store.SetEmailRecipients(r.Context(), a, in.Recipients); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipients": in.Recipients})
}

func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	url, events, hasSecret, err := s.cfg.Store.Webhook(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	deliveries, err := s.cfg.Store.RecentDeliveries(r.Context(), a, 10)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": url, "events": events, "has_secret": hasSecret, "deliveries": deliveries,
	})
}

func (s *Server) handlePutWebhook(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the webhook", nil)
		return
	}
	var in struct {
		URL    string   `json:"url"`
		Secret string   `json:"secret"`
		Events []string `json:"events"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := url.Parse(in.URL)
	if in.URL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"url must be a full http(s) URL", map[string]any{"url": "invalid"})
		return
	}
	if in.URL != "" && !strings.HasPrefix(in.URL, "https://") {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"webhook URL must use https", map[string]any{"url": "https_required"})
		return
	}
	valid := map[string]bool{}
	for _, action := range store.WebhookActions {
		valid[action] = true
	}
	for _, action := range in.Events {
		if !valid[action] {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"events must be shipment event actions", map[string]any{"events": action})
			return
		}
	}
	if err := s.cfg.Store.SetWebhook(r.Context(), a, in.URL, in.Secret, in.Events); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": in.URL, "events": in.Events, "has_secret": in.Secret != ""})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the webhook", nil)
		return
	}
	if err := s.cfg.Store.SetWebhook(r.Context(), a, "", "", nil); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can test the webhook", nil)
		return
	}
	url, _, _, err := s.cfg.Store.Webhook(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if url == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"configure a webhook URL first", nil)
		return
	}
	if err := s.cfg.Store.EnqueueTestDelivery(r.Context(), a, url); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) handleGetSlack(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	url, err := s.cfg.Store.Slack(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The incoming-webhook URL is a posting credential: only admins see it.
	if !a.Role.CanConfigure() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": url != ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "configured": url != ""})
}

func (s *Server) handlePutSlack(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the Slack webhook", nil)
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := url.Parse(in.URL)
	if in.URL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"url must be a full http(s) URL", map[string]any{"url": "invalid"})
		return
	}
	if !strings.HasPrefix(in.URL, "https://") {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Slack webhook URL must use https", map[string]any{"url": "https_required"})
		return
	}
	if err := s.cfg.Store.SetSlack(r.Context(), a, in.URL); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": in.URL})
}

func (s *Server) handleDeleteSlack(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the Slack webhook", nil)
		return
	}
	if err := s.cfg.Store.SetSlack(r.Context(), a, ""); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var e164Re = regexp.MustCompile(`^\+?[0-9]{7,15}$`)

func (s *Server) handleGetWhatsApp(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	cfg, err := s.cfg.Store.WhatsApp(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phone_number_id": cfg.PhoneNumberID,
		"recipients":      cfg.Recipients,
		"api_url":         cfg.APIURL,
		"token_set":       cfg.TokenSet,
	})
}

func (s *Server) handlePutWhatsApp(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the WhatsApp channel", nil)
		return
	}
	var in struct {
		PhoneNumberID string   `json:"phone_number_id"`
		Token         string   `json:"token"`
		Recipients    []string `json:"recipients"`
		APIURL        string   `json:"api_url"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.PhoneNumberID = strings.TrimSpace(in.PhoneNumberID)
	if in.PhoneNumberID == "" || len(in.Recipients) == 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"phone_number_id and at least one recipient are required (delete to disable)",
			map[string]any{"phone_number_id": "required"})
		return
	}
	if !e164Re.MatchString(in.PhoneNumberID) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"phone_number_id must be the numeric id from the WhatsApp Business app", map[string]any{"phone_number_id": "invalid"})
		return
	}
	for _, rcpt := range in.Recipients {
		if !e164Re.MatchString(rcpt) {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"recipients must be E.164 numbers like +5491122334455", map[string]any{"recipients": "invalid"})
			return
		}
	}
	if in.APIURL != "" {
		u, err := url.Parse(in.APIURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"api_url must be a full https URL", map[string]any{"api_url": "https_required"})
			return
		}
	}
	if err := s.cfg.Store.SetWhatsApp(r.Context(), a, in.PhoneNumberID, in.APIURL, in.Recipients, in.Token); err != nil {
		s.internal(w, r, err)
		return
	}
	cfg, err := s.cfg.Store.WhatsApp(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phone_number_id": cfg.PhoneNumberID,
		"recipients":      cfg.Recipients,
		"api_url":         cfg.APIURL,
		"token_set":       cfg.TokenSet,
	})
}

func (s *Server) handleDeleteWhatsApp(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can change the WhatsApp channel", nil)
		return
	}
	if err := s.cfg.Store.ClearWhatsApp(r.Context(), a); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- member management (admins) ---

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can manage members", nil)
		return
	}
	var in struct {
		Email       string      `json:"email"`
		DisplayName string      `json:"display_name"`
		Role        domain.Role `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := s.cfg.Store.CreateUser(r.Context(), a, in.Email, in.DisplayName, in.Role)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can manage members", nil)
		return
	}
	var in struct {
		Role        *domain.Role `json:"role"`
		DisplayName *string      `json:"display_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := s.cfg.Store.UpdateUser(r.Context(), a, r.PathValue("id"), in.Role, in.DisplayName)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can manage members", nil)
		return
	}
	if err := s.cfg.Store.DeleteUser(r.Context(), a, r.PathValue("id")); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- password invites ---

// handleInvite creates a member with a random temporary password, returned once
// in the response. The invited member must replace it on first sign-in.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if !a.Role.CanConfigure() {
		writeError(w, r, http.StatusForbidden, "forbidden", "only admins can manage members", nil)
		return
	}
	var in struct {
		Email       string      `json:"email"`
		DisplayName string      `json:"display_name"`
		Role        domain.Role `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, temp, err := s.cfg.Store.InviteUser(r.Context(), a, in.Email, in.DisplayName, in.Role)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "temporary_password": temp})
}

// handlePasswordLogin signs a member in with email + password (members invited
// with a temporary password). It answers the same session shape as demo login,
// plus `must_change_password` so the UI can force a password change.
func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(in.Email))
	if !s.logins.allow(key, s.now()) || !s.logins.allow("ip:"+s.clientIP(r), s.now()) {
		writeError(w, r, http.StatusTooManyRequests, "too_many_requests", "too many sign-in attempts — try again later", nil)
		return
	}
	id, mustChange, err := s.cfg.Store.LoginByPassword(r.Context(), in.Email, in.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		writeError(w, r, http.StatusUnauthorized, "bad_credentials", "invalid email or password", nil)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.issueSessionWith(w, r, id, map[string]any{"must_change_password": mustChange})
}

// handleChangePassword lets the signed-in member replace their password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.cfg.Store.ChangePassword(r.Context(), actor(r), in.Current, in.Next)
	switch {
	case errors.Is(err, store.ErrBadCredentials):
		writeError(w, r, http.StatusUnauthorized, "bad_credentials", "current password is wrong", nil)
	case errors.Is(err, store.ErrWeakPassword):
		writeError(w, r, http.StatusUnprocessableEntity, "validation_failed", err.Error(),
			map[string]any{"fields": []domain.FieldError{{Field: "new_password", Message: err.Error()}}})
	case err != nil:
		s.writeDomainError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleTrack serves the unauthenticated public tracking view. Any lookup
// failure returns the same 404 — the endpoint must not leak whether a token
// exists.
func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	sh, err := s.cfg.Store.TrackShipment(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "unknown tracking link", nil)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

// handleRotateTrackLink invalidates the current public tracking link and
// returns a fresh token.
func (s *Server) handleRotateTrackLink(w http.ResponseWriter, r *http.Request) {
	token, err := s.cfg.Store.RotateTrackToken(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"track_token": token})
}
