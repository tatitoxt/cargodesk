// Package domain holds request inputs and their validation. Validation is
// pure: callers pass the clock and the organization's timezone.
package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tatitoxt/cargodesk/api/internal/sla"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

type Role string

const (
	Viewer      Role = "viewer"
	Coordinator Role = "coordinator"
	Admin       Role = "admin"
)

// CanEdit reports whether the role may create or change shipments.
func (r Role) CanEdit() bool { return r == Coordinator || r == Admin }

// CanExport reports whether the role may download the CSV export.
func (r Role) CanExport() bool { return r == Coordinator || r == Admin }

// CanConfigure reports whether the role may change organization settings.
func (r Role) CanConfigure() bool { return r == Admin }

var CargoTypes = []string{"general", "palletized", "refrigerated", "hazardous", "bulk", "oversized"}

type FieldError = workflow.FieldError

// ValidationError lists every invalid field of a request.
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

type collector struct{ fields []FieldError }

func (c *collector) add(field, msg string) {
	c.fields = append(c.fields, FieldError{Field: field, Message: msg})
}

func (c *collector) text(field, v string, required bool, max int) {
	v = strings.TrimSpace(v)
	switch {
	case required && v == "":
		c.add(field, "is required")
	case utf8.RuneCountInString(v) > max:
		c.add(field, fmt.Sprintf("must be at most %d characters", max))
	}
}

func (c *collector) err() error {
	if len(c.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: c.fields}
}

// CreateShipmentInput is the body of POST /api/v1/shipments.
type CreateShipmentInput struct {
	AccountName    string       `json:"account_name"`
	ContactLabel   string       `json:"contact_label"`
	Origin         string       `json:"origin"`
	Destination    string       `json:"destination"`
	CargoType      string       `json:"cargo_type"`
	RequiredDate   string       `json:"required_date"`
	Notes          string       `json:"notes"`
	Priority       sla.Priority `json:"priority"`
	OwnerID        *string      `json:"owner_id"`
	NextActionAt   *time.Time   `json:"next_action_at"`
	NextActionNote string       `json:"next_action_note"`
}

// Normalize trims text fields and applies defaults.
func (in *CreateShipmentInput) Normalize() {
	for _, p := range []*string{&in.AccountName, &in.ContactLabel, &in.Origin, &in.Destination, &in.CargoType, &in.RequiredDate, &in.Notes, &in.NextActionNote} {
		*p = strings.TrimSpace(*p)
	}
	if in.Priority == "" {
		in.Priority = sla.Normal
	}
	if in.OwnerID != nil && strings.TrimSpace(*in.OwnerID) == "" {
		in.OwnerID = nil
	}
}

// Validate returns *ValidationError. The required date must be a real
// calendar date and not before today in the operating timezone.
func (in CreateShipmentInput) Validate(now time.Time, loc *time.Location) error {
	var c collector
	c.text("account_name", in.AccountName, true, 120)
	c.text("contact_label", in.ContactLabel, true, 120)
	c.text("origin", in.Origin, true, 200)
	c.text("destination", in.Destination, true, 200)
	c.text("notes", in.Notes, false, 2000)
	c.text("next_action_note", in.NextActionNote, false, 200)
	if in.CargoType == "" {
		c.add("cargo_type", "is required")
	} else if !contains(CargoTypes, in.CargoType) {
		c.add("cargo_type", "must be one of "+strings.Join(CargoTypes, ", "))
	}
	if !in.Priority.Valid() {
		c.add("priority", "must be one of low, normal, high, urgent")
	}
	if in.OwnerID != nil && !IsUUID(*in.OwnerID) {
		c.add("owner_id", "must be a user ID")
	}
	switch d, err := ParseDate(in.RequiredDate, loc); {
	case in.RequiredDate == "":
		c.add("required_date", "is required")
	case err != nil:
		c.add("required_date", "must be a valid date in YYYY-MM-DD format")
	default:
		today, _ := sla.DayBounds(now, loc)
		if d.Before(today) {
			c.add("required_date", "cannot be in the past")
		}
	}
	return c.err()
}

// ParseDate parses YYYY-MM-DD as midnight in loc, rejecting impossible
// dates such as 2026-02-30.
func ParseDate(s string, loc *time.Location) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, loc)
	if err != nil {
		return time.Time{}, err
	}
	if d.Format(time.DateOnly) != s {
		return time.Time{}, fmt.Errorf("invalid date %q", s)
	}
	return d, nil
}

// AssignmentInput is the body of PATCH /api/v1/shipments/{id}/assignment.
// Absent fields keep their current value; owner_id "" clears the owner.
type AssignmentInput struct {
	Version        int           `json:"version"`
	OwnerID        *string       `json:"owner_id"`
	Priority       *sla.Priority `json:"priority"`
	NextActionAt   *time.Time    `json:"next_action_at"`
	ClearNext      bool          `json:"clear_next_action"`
	NextActionNote *string       `json:"next_action_note"`
}

func (in AssignmentInput) Validate() error {
	var c collector
	if in.Version < 1 {
		c.add("version", "is required")
	}
	if in.OwnerID != nil && *in.OwnerID != "" && !IsUUID(*in.OwnerID) {
		c.add("owner_id", "must be a user ID")
	}
	if in.Priority != nil && !in.Priority.Valid() {
		c.add("priority", "must be one of low, normal, high, urgent")
	}
	if in.NextActionNote != nil {
		c.text("next_action_note", *in.NextActionNote, false, 200)
	}
	if in.ClearNext && in.NextActionAt != nil {
		c.add("next_action_at", "cannot be set together with clear_next_action")
	}
	if in.OwnerID == nil && in.Priority == nil && in.NextActionAt == nil && !in.ClearNext && in.NextActionNote == nil {
		c.add("assignment", "nothing to change")
	}
	return c.err()
}

// EventInput is the body of POST /api/v1/shipments/{id}/events.
type EventInput struct {
	Kind string `json:"kind"`
	// note
	Body string `json:"body"`
	// exception
	Version       int        `json:"version"`
	ExceptionKind string     `json:"exception_kind"`
	Reason        string     `json:"reason"`
	FollowUpAt    *time.Time `json:"follow_up_at"`
	OwnerID       *string    `json:"owner_id"`
	// exception_resolved
	ExceptionID string `json:"exception_id"`
	Resolution  string `json:"resolution"`
}

const (
	EventNote              = "note"
	EventException         = "exception"
	EventExceptionResolved = "exception_resolved"
)

var ExceptionKinds = []string{"delay", "damage", "missing_info", "other"}

func (in EventInput) Validate() error {
	var c collector
	switch in.Kind {
	case EventNote:
		c.text("body", in.Body, true, 2000)
	case EventException:
		if in.Version < 1 {
			c.add("version", "is required")
		}
		if !contains(ExceptionKinds, in.ExceptionKind) {
			c.add("exception_kind", "must be one of "+strings.Join(ExceptionKinds, ", "))
		}
		c.text("reason", in.Reason, true, 500)
		if in.FollowUpAt == nil {
			c.add("follow_up_at", "a follow-up date is required")
		}
		if in.OwnerID != nil && *in.OwnerID != "" && !IsUUID(*in.OwnerID) {
			c.add("owner_id", "must be a user ID")
		}
	case EventExceptionResolved:
		if !IsUUID(in.ExceptionID) {
			c.add("exception_id", "must be an exception ID")
		}
		c.text("resolution", in.Resolution, true, 500)
	default:
		c.add("kind", "must be one of note, exception, exception_resolved")
	}
	return c.err()
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// IsUUID reports whether s is a canonical, hyphenated UUID.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
