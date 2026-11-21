// Package store is the PostgreSQL persistence layer. Every method takes the
// caller's organization from an Actor and scopes all SQL to it.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrClosed is returned when changing a shipment in a terminal state.
	ErrClosed = errors.New("shipment is closed")
	// ErrIdempotencyMismatch is returned when a key is reused with a different body.
	ErrIdempotencyMismatch   = errors.New("idempotency key reused with a different request")
	ErrIdempotencyInProgress = errors.New("a request with this idempotency key is still in progress")
)

// VersionConflictError means the client's version is stale.
type VersionConflictError struct{ Current int }

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict: current version is %d", e.Current)
}

// Actor is the authenticated user a request runs as.
type Actor struct {
	UserID      string      `json:"id"`
	Email       string      `json:"email"`
	DisplayName string      `json:"display_name"`
	Role        domain.Role `json:"role"`
	Org         Org         `json:"organization"`
}

type Org struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	// WebhookURL receives a JSON POST per shipment event when set.
	WebhookURL *string `json:"-"`
	// WebhookSecret, when set, signs bodies as X-CargoDesk-Signature: sha256=<hmac>.
	WebhookSecret *string        `json:"-"`
	Loc           *time.Location `json:"-"`
}

func loadLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Open connects and pings the database.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// WebhookActions is every shipment_events.action value a subscription can
// filter to. Keep in sync with shipment_events_action_check.
var WebhookActions = []string{
	"created", "transitioned", "assigned", "note_added",
	"exception_opened", "exception_resolved", "attachment_added", "attachment_removed",
}

// SetWebhook updates the org's notification webhook. Empty URL disables it.
// events nil or empty means every action enqueues a delivery; a non-empty
// list limits deliveries to those actions.
func (s *Store) SetWebhook(ctx context.Context, a Actor, url, secret string, events []string) error {
	var u, sec *string
	if url != "" {
		u = &url
	}
	if secret != "" {
		sec = &secret
	}
	var ev *([]string)
	if len(events) > 0 {
		ev = &events
	}
	_, err := s.pool.Exec(ctx, `UPDATE organizations SET webhook_url = $2, webhook_secret = $3, webhook_events = $4 WHERE id = $1`,
		a.Org.ID, u, sec, ev)
	return err
}

// Webhook returns the org's webhook config (url, subscribed actions — nil means
// all — and whether a secret is set).
func (s *Store) Webhook(ctx context.Context, a Actor) (url string, events []string, hasSecret bool, err error) {
	var u, sec *string
	err = s.pool.QueryRow(ctx, `SELECT webhook_url, webhook_secret, webhook_events FROM organizations WHERE id = $1`,
		a.Org.ID).Scan(&u, &sec, &events)
	if u != nil {
		url = *u
	}
	return url, events, sec != nil, err
}

// Delivery is a queued outbound webhook POST.
type Delivery struct {
	ID        int64
	EventID   int64
	URL       string
	Payload   json.RawMessage
	Attempts  int
	CreatedAt time.Time
}

// DueDeliveries claims up to n pending deliveries whose attempt is due.
func (s *Store) DueDeliveries(ctx context.Context, n int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, event_id, url, payload, attempts, created_at
		FROM notification_deliveries
		WHERE delivered_at IS NULL AND next_attempt_at <= now() AND attempts < 8
		ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventID, &d.URL, &d.Payload, &d.Attempts, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDelivered records a successful POST.
func (s *Store) MarkDelivered(ctx context.Context, id int64, statusCode int) error {
	_, err := s.pool.Exec(ctx, `UPDATE notification_deliveries
		SET delivered_at = now(), status_code = $2, last_error = NULL WHERE id = $1`, id, statusCode)
	return err
}

// RetryDelivery schedules the next attempt with linear backoff (attempts*30s)
// or gives up after the 8th try (delivered_at stays NULL; next_attempt_at is
// pushed far out so it stops matching the due query).
func (s *Store) RetryDelivery(ctx context.Context, id int64, attempts int, errMsg string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notification_deliveries
		SET attempts = $2, last_error = $3,
		    next_attempt_at = CASE WHEN $2 >= 8 THEN '9999-01-01'::timestamptz
		                           ELSE now() + ($2 || ' seconds')::interval * 30 END
		WHERE id = $1`, id, attempts, errMsg)
	return err
}

// RecentDeliveries lists the org's last n deliveries for the settings UI.
type DeliveryLog struct {
	ID          int64      `json:"id"`
	Action      string     `json:"action"`
	StatusCode  *int       `json:"status_code"`
	Attempts    int        `json:"attempts"`
	Delivered   bool       `json:"delivered"`
	LastError   *string    `json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

func (s *Store) RecentDeliveries(ctx context.Context, a Actor, n int) ([]DeliveryLog, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id, d.action, d.status_code, d.attempts,
			d.delivered_at IS NOT NULL, d.last_error, d.created_at, d.delivered_at
		FROM notification_deliveries d
		WHERE d.organization_id = $1 ORDER BY d.id DESC LIMIT $2`, a.Org.ID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryLog{}
	for rows.Next() {
		var l DeliveryLog
		if err := rows.Scan(&l.ID, &l.Action, &l.StatusCode, &l.Attempts, &l.Delivered, &l.LastError, &l.CreatedAt, &l.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// EnqueueTestDelivery queues a synthetic ping so admins can verify the URL.
func (s *Store) EnqueueTestDelivery(ctx context.Context, a Actor, url string) error {
	payload, _ := json.Marshal(map[string]any{
		"action":         "test",
		"organization":   a.Org.Slug,
		"actor":          a.Email,
		"correlation_id": "test-" + a.Org.Slug,
	})
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_deliveries (organization_id, action, url, payload)
		VALUES ($1, 'webhook_test', $2, $3)`, a.Org.ID, url, payload)
	return err
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// SLAPolicy returns the organization's time-to-quote policy, falling back to
// defaults for priorities without a row.
func (s *Store) SLAPolicy(ctx context.Context, a Actor) (sla.Policy, error) {
	return slaPolicy(ctx, s.pool, a.Org.ID, sla.StageQuote)
}

// ScheduleSLAPolicy returns the organization's time-to-schedule policy.
func (s *Store) ScheduleSLAPolicy(ctx context.Context, a Actor) (sla.Policy, error) {
	return slaPolicy(ctx, s.pool, a.Org.ID, sla.StageSchedule)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func slaPolicy(ctx context.Context, q querier, orgID string, stage sla.Stage) (sla.Policy, error) {
	p := sla.DefaultPolicy()
	if stage == sla.StageSchedule {
		p = sla.DefaultSchedulePolicy()
	}
	rows, err := q.Query(ctx, `SELECT priority, hours FROM sla_policies WHERE organization_id = $1 AND stage = $2`, orgID, string(stage))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pr string
		var h int
		if err := rows.Scan(&pr, &h); err != nil {
			return nil, err
		}
		p[sla.Priority(pr)] = h
	}
	return p, rows.Err()
}

// SetSLAPolicy replaces the quote-stage policy. It applies to requests
// created, or whose priority changes, afterwards; existing due dates are not
// rewritten.
func (s *Store) SetSLAPolicy(ctx context.Context, a Actor, p sla.Policy) error {
	return s.setSLAStagePolicy(ctx, a, sla.StageQuote, p, "hours")
}

// SetScheduleSLAPolicy replaces the schedule-stage policy.
func (s *Store) SetScheduleSLAPolicy(ctx context.Context, a Actor, p sla.Policy) error {
	return s.setSLAStagePolicy(ctx, a, sla.StageSchedule, p, "schedule_hours")
}

func (s *Store) setSLAStagePolicy(ctx context.Context, a Actor, stage sla.Stage, p sla.Policy, field string) error {
	if err := p.Validate(); err != nil {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: field, Message: err.Error()}}}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, pr := range sla.Priorities {
			if _, err := tx.Exec(ctx, `INSERT INTO sla_policies (organization_id, stage, priority, hours) VALUES ($1, $2, $3, $4)
				ON CONFLICT (organization_id, stage, priority) DO UPDATE SET hours = EXCLUDED.hours`, a.Org.ID, string(stage), pr, p[pr]); err != nil {
				return err
			}
		}
		return nil
	})
}

type UserRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type User struct {
	ID          string      `json:"id"`
	Email       string      `json:"email"`
	DisplayName string      `json:"display_name"`
	Role        domain.Role `json:"role"`
}

// Users lists the organization's users (owner candidates).
func (s *Store) Users(ctx context.Context, a Actor) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, email, display_name, role FROM users WHERE organization_id = $1 ORDER BY display_name`, a.Org.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		err := r.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
		return u, err
	})
}

func userInOrg(ctx context.Context, q querier, orgID, userID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE organization_id = $1 AND id = $2)`, orgID, userID).Scan(&ok)
	return ok, err
}

func unknownOwner(field string) error {
	return &domain.ValidationError{Fields: []domain.FieldError{{Field: field, Message: "is not a user of this organization"}}}
}

// BusinessCalendar returns the org's business-hours calendar, or nil when none
// is configured (SLAs then count wall-clock hours).
func (s *Store) BusinessCalendar(ctx context.Context, a Actor) (*sla.Calendar, error) {
	return businessCalendar(ctx, s.pool, a.Org.ID)
}

func businessCalendar(ctx context.Context, q querier, orgID string) (*sla.Calendar, error) {
	var c sla.Calendar
	var days []int16
	var holidays []time.Time
	err := q.QueryRow(ctx, `SELECT days, start_minute, end_minute, holidays FROM business_calendars
		WHERE organization_id = $1`, orgID).
		Scan(&days, &c.StartMinute, &c.EndMinute, &holidays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, d := range days {
		c.Days = append(c.Days, int(d))
	}
	for _, h := range holidays {
		c.Holidays = append(c.Holidays, h.Format("2006-01-02"))
	}
	return &c, nil
}

// SetBusinessCalendar replaces the org's calendar (validated); later due dates
// count business minutes in the organization's timezone.
func (s *Store) SetBusinessCalendar(ctx context.Context, a Actor, c sla.Calendar) error {
	if err := c.Validate(); err != nil {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "calendar", Message: err.Error()}}}
	}
	if c.Holidays == nil {
		c.Holidays = []string{}
	}
	days := make([]int16, len(c.Days))
	for i, d := range c.Days {
		days[i] = int16(d)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO business_calendars
		(organization_id, days, start_minute, end_minute, holidays, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5::date[], $6, now())
		ON CONFLICT (organization_id) DO UPDATE SET days = EXCLUDED.days,
		start_minute = EXCLUDED.start_minute, end_minute = EXCLUDED.end_minute,
		holidays = EXCLUDED.holidays, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		a.Org.ID, days, c.StartMinute, c.EndMinute, c.Holidays, a.UserID)
	return err
}

// ClearBusinessCalendar removes the calendar; SLAs revert to wall-clock hours.
func (s *Store) ClearBusinessCalendar(ctx context.Context, a Actor) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM business_calendars WHERE organization_id = $1`, a.Org.ID)
	return err
}

// dueAtStage computes an SLA deadline for one stage: business minutes when a
// calendar is set in the org timezone, else plain hours from the policy.
func dueAtStage(ctx context.Context, q querier, a Actor, from time.Time, pr sla.Priority, stage sla.Stage) (time.Time, error) {
	policy, err := slaPolicy(ctx, q, a.Org.ID, stage)
	if err != nil {
		return time.Time{}, err
	}
	cal, err := businessCalendar(ctx, q, a.Org.ID)
	if err != nil {
		return time.Time{}, err
	}
	if cal == nil {
		return sla.DueAt(from, pr, policy), nil
	}
	loc, err := time.LoadLocation(a.Org.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return sla.DueAtBusiness(from, policy[pr], *cal, loc), nil
}
