package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer sends one email. The dispatcher treats an error as retryable.
type Mailer interface {
	Send(ctx context.Context, to []string, subject, body string) error
}

// SMTPMailer delivers via a plain SMTP or implicit-TLS relay. User/Pass are
// optional (plain auth is only attempted when both are set).
type SMTPMailer struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	TLS      bool // implicit TLS (e.g. port 465); STARTTLS is tried otherwise
	Timeout  time.Duration
	sendMail func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
	dialTLS  func(ctx context.Context, addr string) (*smtp.Client, error)
}

// Send builds an RFC 822 message and submits it.
func (m *SMTPMailer) Send(ctx context.Context, to []string, subject, body string) error {
	if len(to) == 0 {
		return fmt.Errorf("no recipients")
	}
	port := m.Port
	if port == 0 {
		port = 25
	}
	addr := net.JoinHostPort(m.Host, fmt.Sprint(port))
	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		m.From, strings.Join(to, ", "), subject, body))

	if m.TLS {
		c, err := m.tlsClient(ctx, addr)
		if err != nil {
			return err
		}
		defer c.Close()
		if m.User != "" {
			if err := c.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		if err := c.Mail(m.From); err != nil {
			return err
		}
		for _, rcpt := range to {
			if err := c.Rcpt(rcpt); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(msg); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return c.Quit()
	}

	sendMail := m.sendMail
	if sendMail == nil {
		sendMail = smtp.SendMail
	}
	var auth smtp.Auth
	if m.User != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	return sendMail(addr, auth, m.From, to, msg)
}

func (m *SMTPMailer) tlsClient(ctx context.Context, addr string) (*smtp.Client, error) {
	if m.dialTLS != nil {
		return m.dialTLS(ctx, addr)
	}
	d := &tls.Dialer{Config: &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}}
	if m.Timeout > 0 {
		d.NetDialer = &net.Dialer{Timeout: m.Timeout}
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp tls dial: %w", err)
	}
	host, _, _ := net.SplitHostPort(addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// EmailSubject renders the subject line for an event payload.
func EmailSubject(payload json.RawMessage) string {
	var p struct {
		Event      string `json:"event"`
		ShipmentID string `json:"shipment_id"`
		Org        string `json:"organization"`
	}
	_ = json.Unmarshal(payload, &p)
	subj := "CargoDesk: " + p.Event
	if p.ShipmentID != "" {
		subj += " shipment " + p.ShipmentID
	}
	if p.Org != "" {
		subj += " (" + p.Org + ")"
	}
	return subj
}

// EmailBody renders a plain-text body for an event payload.
func EmailBody(payload json.RawMessage) string {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	var b strings.Builder
	for _, k := range []string{"event", "shipment_id", "organization", "actor", "from_state", "to_state", "reason", "correlation_id", "at"} {
		if v, ok := p[k]; ok && v != nil {
			fmt.Fprintf(&b, "%s: %v\n", k, v)
		}
	}
	if data, ok := p["data"]; ok && data != nil {
		raw, _ := json.MarshalIndent(data, "data: ", "  ")
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}
