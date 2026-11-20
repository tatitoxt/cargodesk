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

// pendingStatus marks a key whose first request is still running.
const pendingStatus = 0

// pendingTTL bounds how long a crashed request can block its key.
const pendingTTL = time.Minute

// ReserveIdempotent claims key before the request runs, so concurrent
// retries cannot both execute it. It returns (nil, nil) when the caller now
// owns the key and must CompleteIdempotent or ReleaseIdempotent it; the
// stored response when the request already succeeded; ErrIdempotencyInProgress
// while another request holds the key; or ErrIdempotencyMismatch.
func (s *Store) ReserveIdempotent(ctx context.Context, a Actor, key, scope string, hash []byte, now time.Time) (*StoredResponse, error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE organization_id = $1 AND user_id = $2 AND key = $3 AND status_code = $4 AND created_at < $5`,
		a.Org.ID, a.UserID, key, pendingStatus, now.Add(-pendingTTL)); err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (organization_id, user_id, key, scope, request_hash, status_code, response, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, '{}', $7) ON CONFLICT DO NOTHING`, a.Org.ID, a.UserID, key, scope, hash, pendingStatus, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}
	var stored []byte
	var r StoredResponse
	err = s.pool.QueryRow(ctx, `SELECT request_hash, status_code, response FROM idempotency_keys
		WHERE organization_id = $1 AND user_id = $2 AND key = $3`, a.Org.ID, a.UserID, key).Scan(&stored, &r.Status, &r.Body)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrIdempotencyInProgress // released between our insert and select; client may retry
	case err != nil:
		return nil, err
	case !bytes.Equal(stored, hash):
		return nil, ErrIdempotencyMismatch
	case r.Status == pendingStatus:
		return nil, ErrIdempotencyInProgress
	}
	return &r, nil
}

// CompleteIdempotent stores the response for a reserved key.
func (s *Store) CompleteIdempotent(ctx context.Context, a Actor, key string, status int, body []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE idempotency_keys SET status_code = $4, response = $5
		WHERE organization_id = $1 AND user_id = $2 AND key = $3 AND status_code = $6`,
		a.Org.ID, a.UserID, key, status, body, pendingStatus)
	return err
}

// ReleaseIdempotent frees a reserved key whose request did not succeed, so
// the client can retry it (possibly with a corrected body).
func (s *Store) ReleaseIdempotent(ctx context.Context, a Actor, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE organization_id = $1 AND user_id = $2 AND key = $3 AND status_code = $4`, a.Org.ID, a.UserID, key, pendingStatus)
	return err
}
