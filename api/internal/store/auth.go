package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// LoginUser is a seeded user offered on the demo sign-in page.
type LoginUser struct {
	User
	OrganizationName string `json:"organization_name"`
}

func (s *Store) LoginUsers(ctx context.Context) ([]LoginUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.email, u.display_name, u.role, o.name
		FROM users u JOIN organizations o ON o.id = u.organization_id
		ORDER BY o.name, CASE u.role WHEN 'coordinator' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, u.display_name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LoginUser, error) {
		var u LoginUser
		err := r.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.OrganizationName)
		return u, err
	})
}

func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CreateSession stores a hashed random token for userID and returns the
// raw token for the cookie.
func (s *Store) CreateSession(ctx context.Context, userID string, now time.Time, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	tag, err := s.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, expires_at)
		SELECT $1, id, $3, $4 FROM users WHERE id = $2`, HashToken(token), userID, now, now.Add(ttl))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// ActorBySession resolves a raw session token.
func (s *Store) ActorBySession(ctx context.Context, token string, now time.Time) (Actor, error) {
	var a Actor
	err := s.pool.QueryRow(ctx, `SELECT u.id, u.email, u.display_name, u.role, o.id, o.slug, o.name, o.timezone
		FROM sessions s JOIN users u ON u.id = s.user_id JOIN organizations o ON o.id = u.organization_id
		WHERE s.token_hash = $1 AND s.expires_at > $2`, HashToken(token), now).
		Scan(&a.UserID, &a.Email, &a.DisplayName, &a.Role, &a.Org.ID, &a.Org.Slug, &a.Org.Name, &a.Org.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return Actor{}, ErrNotFound
	}
	a.Org.Loc = loadLoc(a.Org.Timezone)
	return a, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, HashToken(token))
	return err
}
