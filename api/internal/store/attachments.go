package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Attachment is the metadata row for one uploaded file. Bytes live on the
// configured blob dir; this table is the tenant-scoped source of truth.
type Attachment struct {
	ID          string    `json:"id"`
	ShipmentID  string    `json:"shipment_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	ByteSize    int64     `json:"byte_size"`
	SHA256      string    `json:"sha256"`
	StorageKey  string    `json:"-"`
	UploadedBy  string    `json:"uploaded_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// SaveAttachment registers a file for a shipment in the actor's org and logs
// the audit event in the same transaction.
func (s *Store) SaveAttachment(ctx context.Context, a Actor, shipmentID string, in Attachment, now time.Time, corr string) (Attachment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attachment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM shipments WHERE id = $1 AND organization_id = $2)`,
		shipmentID, a.Org.ID).Scan(&exists); err != nil {
		return Attachment{}, err
	}
	if !exists {
		return Attachment{}, ErrNotFound
	}

	err = tx.QueryRow(ctx, `INSERT INTO attachments
		(organization_id, shipment_id, filename, content_type, byte_size, sha256, storage_key, uploaded_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, created_at`,
		a.Org.ID, shipmentID, in.Filename, in.ContentType, in.ByteSize, in.SHA256, in.StorageKey, a.UserID, now).
		Scan(&in.ID, &in.CreatedAt)
	if err != nil {
		return Attachment{}, err
	}
	in.ShipmentID = shipmentID
	in.UploadedBy = a.UserID

	if _, err := insertEvent(ctx, tx, a, EventRecord{
		ShipmentID: shipmentID, Action: "attachment_added", CorrelationID: corr, At: now,
		Data: map[string]any{"filename": in.Filename, "byte_size": in.ByteSize, "attachment_id": in.ID},
	}); err != nil {
		return Attachment{}, err
	}
	return in, tx.Commit(ctx)
}

// ListAttachments returns metadata for a shipment's files, newest first.
func (s *Store) ListAttachments(ctx context.Context, a Actor, shipmentID string) ([]Attachment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, shipment_id, filename, content_type, byte_size, sha256, storage_key, uploaded_by, created_at
		FROM attachments WHERE organization_id = $1 AND shipment_id = $2 ORDER BY created_at DESC`, a.Org.ID, shipmentID)
	if err != nil {
		return nil, err
	}
	out, err := scanAttachments(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM shipments WHERE id = $1 AND organization_id = $2)`,
			shipmentID, a.Org.ID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return out, nil
}

// GetAttachment returns metadata + storage key for one file in the actor's org.
func (s *Store) GetAttachment(ctx context.Context, a Actor, shipmentID, attachmentID string) (Attachment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, shipment_id, filename, content_type, byte_size, sha256, storage_key, uploaded_by, created_at
		FROM attachments WHERE organization_id = $1 AND shipment_id = $2 AND id = $3`,
		a.Org.ID, shipmentID, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	out, err := scanAttachments(rows)
	if err != nil {
		return Attachment{}, err
	}
	if len(out) == 0 {
		return Attachment{}, ErrNotFound
	}
	return out[0], nil
}

// DeleteAttachment removes the metadata row and logs the audit event; the
// handler deletes the blob after a successful call.
func (s *Store) DeleteAttachment(ctx context.Context, a Actor, shipmentID, attachmentID string, now time.Time, corr string) (Attachment, error) {
	att, err := s.GetAttachment(ctx, a, shipmentID, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attachment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM attachments WHERE id = $1 AND organization_id = $2`,
		attachmentID, a.Org.ID); err != nil {
		return Attachment{}, err
	}
	if _, err := insertEvent(ctx, tx, a, EventRecord{
		ShipmentID: shipmentID, Action: "attachment_removed", CorrelationID: corr, At: now,
		Data: map[string]any{"filename": att.Filename, "attachment_id": att.ID},
	}); err != nil {
		return Attachment{}, err
	}
	return att, tx.Commit(ctx)
}

// SetAttachmentRetention configures how many days an org keeps attachments
// before the purge pass deletes them; 0 keeps them forever.
func (s *Store) SetAttachmentRetention(ctx context.Context, a Actor, days int) error {
	if days < 0 {
		return fmt.Errorf("retention days must be >= 0")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE organizations SET attachment_retention_days = $1 WHERE id = $2`, days, a.Org.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgedAttachment is one attachment row deleted by a retention run. The
// caller removes StorageKey's blob once the database deletion committed.
type PurgedAttachment struct {
	OrgID        string
	ShipmentID   string
	StorageKey   string
	Filename     string
	AttachmentID string
}

// PurgeExpiredAttachments deletes attachment rows older than each org's
// configured retention window and returns their storage keys for blob
// cleanup. Each deletion is audited with an attachment_removed event
// attributed to the org's earliest user (shipment_events.actor_id is
// NOT NULL and references a real user), under a "retention-purge"
// correlation id. Orgs with retention_days = 0 are skipped.
func (s *Store) PurgeExpiredAttachments(ctx context.Context, now time.Time) ([]PurgedAttachment, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.id, a.id, a.shipment_id, a.filename, a.storage_key
		FROM organizations o JOIN attachments a ON a.organization_id = o.id
		WHERE o.attachment_retention_days > 0
		  AND a.created_at < $1::timestamptz - make_interval(days => o.attachment_retention_days)
		ORDER BY o.id, a.created_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byOrg := map[string][]PurgedAttachment{}
	var orgOrder []string
	for rows.Next() {
		var p PurgedAttachment
		if err := rows.Scan(&p.OrgID, &p.AttachmentID, &p.ShipmentID, &p.Filename, &p.StorageKey); err != nil {
			return nil, err
		}
		if _, seen := byOrg[p.OrgID]; !seen {
			orgOrder = append(orgOrder, p.OrgID)
		}
		byOrg[p.OrgID] = append(byOrg[p.OrgID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []PurgedAttachment
	for _, orgID := range orgOrder {
		// The audit trail needs a real user per org; uploaded files imply at
		// least one exists, but skip defensively rather than fail the run.
		var userID string
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM users WHERE organization_id = $1 ORDER BY created_at LIMIT 1`, orgID).Scan(&userID)
		if err != nil {
			continue
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return out, err
		}
		actor := Actor{UserID: userID, Org: Org{ID: orgID}}
		for _, p := range byOrg[orgID] {
			if _, err := tx.Exec(ctx,
				`DELETE FROM attachments WHERE id = $1 AND organization_id = $2`,
				p.AttachmentID, orgID); err != nil {
				_ = tx.Rollback(ctx)
				return out, err
			}
			if _, err := insertEvent(ctx, tx, actor, EventRecord{
				ShipmentID: p.ShipmentID, Action: "attachment_removed",
				CorrelationID: "retention-purge", At: now,
				Data: map[string]any{
					"filename": p.Filename, "attachment_id": p.AttachmentID,
					"reason": "retention",
				},
			}); err != nil {
				_ = tx.Rollback(ctx)
				return out, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return out, err
		}
		out = append(out, byOrg[orgID]...)
	}
	return out, nil
}

func scanAttachment(r pgx.CollectableRow) (Attachment, error) {
	var a Attachment
	err := r.Scan(&a.ID, &a.ShipmentID, &a.Filename, &a.ContentType, &a.ByteSize,
		&a.SHA256, &a.StorageKey, &a.UploadedBy, &a.CreatedAt)
	return a, err
}

func scanAttachments(rows pgx.Rows) ([]Attachment, error) {
	return pgx.CollectRows(rows, scanAttachment)
}
