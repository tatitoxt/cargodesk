// Package notify posts queued webhook deliveries to each organization's
// configured endpoint. Deliveries are written inside the same transaction as
// the shipment event they describe (see store.insertEvent), so this worker is
// the only piece that can fail without losing the notification.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/store"
)

// Dispatcher polls for due deliveries and POSTs them.
type Dispatcher struct {
	Store    *store.Store
	Log      *slog.Logger
	Client   *http.Client
	Interval time.Duration
	Batch    int
	// Signing is shared by the dispatcher and the "send test" endpoint.
	Sign func(payload []byte, secret string) string
}

// Run loops until ctx is cancelled. Errors in one batch are logged, not fatal.
func (d *Dispatcher) Run(ctx context.Context) {
	if d.Interval <= 0 {
		d.Interval = 2 * time.Second
	}
	if d.Batch <= 0 {
		d.Batch = 20
	}
	if d.Client == nil {
		d.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if d.Sign == nil {
		d.Sign = func(payload []byte, secret string) string {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write(payload)
			return "sha256=" + hex.EncodeToString(m.Sum(nil))
		}
	}
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for {
		d.flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Dispatcher) flush(ctx context.Context) {
	ds, err := d.Store.DueDeliveries(ctx, d.Batch)
	if err != nil {
		d.Log.Warn("deliveries poll failed", "error", err)
		return
	}
	for _, del := range ds {
		if err := d.post(ctx, del); err != nil {
			d.Log.Warn("webhook delivery failed", "id", del.ID, "attempts", del.Attempts+1, "error", err)
			if err := d.Store.RetryDelivery(ctx, del.ID, del.Attempts+1, err.Error()); err != nil {
				d.Log.Warn("retry record failed", "id", del.ID, "error", err)
			}
			continue
		}
	}
}

func (d *Dispatcher) post(ctx context.Context, del store.Delivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, del.URL, bytes.NewReader(del.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cargodesk-webhook/1.0")
	if sec, ok, err := d.deliverySecret(ctx, del); err == nil && ok {
		req.Header.Set("X-CargoDesk-Signature", d.Sign(del.Payload, sec))
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return d.Store.MarkDelivered(ctx, del.ID, resp.StatusCode)
}

// deliverySecret looks up the org's current secret for the signature; the URL
// is frozen at enqueue time but the secret is read live so rotating it takes
// effect for queued deliveries too.
func (d *Dispatcher) deliverySecret(ctx context.Context, del store.Delivery) (string, bool, error) {
	var sec *string
	err := d.Store.Pool().QueryRow(ctx,
		`SELECT o.webhook_secret FROM organizations o
		 JOIN notification_deliveries n ON n.organization_id = o.id WHERE n.id = $1`, del.ID).Scan(&sec)
	if err != nil || sec == nil {
		return "", false, err
	}
	return *sec, true, nil
}
