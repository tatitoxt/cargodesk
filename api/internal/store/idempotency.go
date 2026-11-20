package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// StoredResponse is a response replayed for a repeated Idempotency-Key.
type StoredResponse struct {
	Status int
	Body   json.RawMessage
}

func RequestHash(scope string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(scope))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// LookupIdempotent returns the stored response for key, nil if unseen, or
// ErrIdempotencyMismatch if the key was used for a different request.
func (s *Store) LookupIdempotent(ctx context.Context, a Actor, key string, hash []byte) (*StoredResponse, error) {
	var stored []byte
	var r StoredResponse
	err := s.pool.QueryRow(ctx, `SELECT request_hash, status_code, response FROM idempotency_keys
		WHERE organization_id = $1 AND user_id = $2 AND key = $3`, a.Org.ID, a.UserID, key).Scan(&stored, &r.Status, &r.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(stored, hash) {
		return nil, ErrIdempotencyMismatch
	}
	return &r, nil
}

// SaveIdempotent stores the first response for key; later saves are ignored.
func (s *Store) SaveIdempotent(ctx context.Context, a Actor, key, scope string, hash []byte, status int, body []byte, now time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (organization_id, user_id, key, scope, request_hash, status_code, response, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`, a.Org.ID, a.UserID, key, scope, hash, status, body, now)
	return err
}
