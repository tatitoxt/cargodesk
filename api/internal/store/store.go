// Package store is the PostgreSQL persistence layer. Every method takes the
// caller's organization from an Actor and scopes all SQL to it.
package store

import (
	"context"
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
	ID       string         `json:"id"`
	Slug     string         `json:"slug"`
	Name     string         `json:"name"`
	Timezone string         `json:"timezone"`
	Loc      *time.Location `json:"-"`
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

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// SLAPolicy returns the organization's policy, falling back to defaults for
// priorities without a row.
func (s *Store) SLAPolicy(ctx context.Context, a Actor) (sla.Policy, error) {
	return slaPolicy(ctx, s.pool, a.Org.ID)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func slaPolicy(ctx context.Context, q querier, orgID string) (sla.Policy, error) {
	p := sla.DefaultPolicy()
	rows, err := q.Query(ctx, `SELECT priority, hours FROM sla_policies WHERE organization_id = $1`, orgID)
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

// SetSLAPolicy replaces the policy. It applies to requests created, or whose
// priority changes, afterwards; existing due dates are not rewritten.
func (s *Store) SetSLAPolicy(ctx context.Context, a Actor, p sla.Policy) error {
	if err := p.Validate(); err != nil {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "hours", Message: err.Error()}}}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, pr := range sla.Priorities {
			if _, err := tx.Exec(ctx, `INSERT INTO sla_policies (organization_id, priority, hours) VALUES ($1, $2, $3)
				ON CONFLICT (organization_id, priority) DO UPDATE SET hours = EXCLUDED.hours`, a.Org.ID, pr, p[pr]); err != nil {
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
