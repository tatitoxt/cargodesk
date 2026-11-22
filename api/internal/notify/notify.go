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
	"encoding/json"
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
	// Mailer, when set, delivers channel='email' rows; without it they retry
	// until their attempts run out.
	Mailer Mailer
	// Signing is shared by the dispatcher and the "send test" endpoint.
	Sign func(payload []byte, secret string) string
}

// NewHTTPClient never follows redirects: a 3xx would turn the POST into a GET
// and drop the payload, so it is recorded as a failed attempt instead.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
		d.Client = NewHTTPClient()
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
		var err error
		switch del.Channel {
		case "email":
			err = d.mail(ctx, del)
		case "whatsapp":
			err = d.whatsapp(ctx, del)
		default:
			err = d.post(ctx, del)
		}
		if err != nil {
			d.Log.Warn("delivery failed", "id", del.ID, "channel", del.Channel, "attempts", del.Attempts+1, "error", err)
			if err := d.Store.RetryDelivery(ctx, del.ID, del.Attempts+1, err.Error()); err != nil {
				d.Log.Warn("retry record failed", "id", del.ID, "error", err)
			}
			continue
		}
	}
}

// whatsapp posts a Cloud API message to each recipient. The payload holds the
// rendered {"text": ...} frozen at enqueue; the access token is read live so
// rotating it takes effect for already-queued deliveries. Every recipient must
// succeed before the row is marked delivered.
func (d *Dispatcher) whatsapp(ctx context.Context, del store.Delivery) error {
	token, err := d.whatsappToken(ctx, del)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("whatsapp channel not configured (no access token)")
	}
	var msg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(del.Payload, &msg); err != nil || msg.Text == "" {
		return fmt.Errorf("invalid whatsapp payload: %w", err)
	}
	lastCode := 0
	for _, rcpt := range del.Rcpt {
		body, err := json.Marshal(map[string]any{
			"messaging_product": "whatsapp",
			"to":                rcpt,
			"type":              "text",
			"text":              map[string]any{"body": msg.Text},
		})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, del.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", "cargodesk-webhook/1.0")
		resp, err := d.Client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("whatsapp api returned %d for %s", resp.StatusCode, rcpt)
		}
		lastCode = resp.StatusCode
	}
	return d.Store.MarkDelivered(ctx, del.ID, lastCode)
}

// whatsappToken reads the org's live WhatsApp access token.
func (d *Dispatcher) whatsappToken(ctx context.Context, del store.Delivery) (string, error) {
	var tok *string
	err := d.Store.Pool().QueryRow(ctx,
		`SELECT o.whatsapp_token FROM organizations o
		 JOIN notification_deliveries n ON n.organization_id = o.id WHERE n.id = $1`, del.ID).Scan(&tok)
	if err != nil || tok == nil {
		return "", err
	}
	return *tok, nil
}

// mail sends an email delivery through the configured Mailer and marks it
// delivered (status_code 0 distinguishes it from HTTP responses in the log).
func (d *Dispatcher) mail(ctx context.Context, del store.Delivery) error {
	if d.Mailer == nil {
		return fmt.Errorf("email channel not configured (SMTP_HOST unset)")
	}
	if err := d.Mailer.Send(ctx, del.Rcpt, EmailSubject(del.Payload), EmailBody(del.Payload)); err != nil {
		return err
	}
	return d.Store.MarkDelivered(ctx, del.ID, 0)
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
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
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
