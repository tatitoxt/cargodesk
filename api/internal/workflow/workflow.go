// Package workflow defines the shipment state machine and the input each
// transition requires. It is pure: no I/O, no clock.
package workflow

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/currency"
)

type State string

const (
	New       State = "new"
	NeedsInfo State = "needs_info"
	Quoted    State = "quoted"
	Accepted  State = "accepted"
	Scheduled State = "scheduled"
	InTransit State = "in_transit"
	Delivered State = "delivered"
	Cancelled State = "cancelled"
	Lost      State = "lost"
)

// AllStates lists every state in workflow order.
var AllStates = []State{New, NeedsInfo, Quoted, Accepted, Scheduled, InTransit, Delivered, Cancelled, Lost}

var transitions = map[State][]State{
	New:       {NeedsInfo, Quoted, Cancelled, Lost},
	NeedsInfo: {New, Quoted, Cancelled, Lost},
	Quoted:    {Accepted, NeedsInfo, Cancelled, Lost},
	Accepted:  {Scheduled, Cancelled},
	Scheduled: {InTransit, Cancelled},
	InTransit: {Delivered},
	Delivered: {},
	Cancelled: {},
	Lost:      {},
}

func (s State) Valid() bool { _, ok := transitions[s]; return ok }

func (s State) Terminal() bool { return s.Valid() && len(transitions[s]) == 0 }

// AwaitingQuote reports whether the internal "time to quote" SLA still runs.
func (s State) AwaitingQuote() bool { return s == New || s == NeedsInfo }

// Allowed returns the states reachable from s, in a stable order.
func Allowed(s State) []State { return slices.Clone(transitions[s]) }

func CanTransition(from, to State) bool { return slices.Contains(transitions[from], to) }

type QuoteInput struct {
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
}

type EvidenceInput struct {
	ReceivedBy  string     `json:"received_by"`
	DeliveredAt *time.Time `json:"delivered_at"`
	Note        string     `json:"note,omitempty"`
}

// TransitionInput is the command body of POST /shipments/{id}/transitions.
type TransitionInput struct {
	To       State          `json:"to"`
	Reason   string         `json:"reason,omitempty"`
	Quote    *QuoteInput    `json:"quote,omitempty"`
	PickupAt *time.Time     `json:"pickup_at,omitempty"`
	Carrier  string         `json:"carrier,omitempty"`
	Evidence *EvidenceInput `json:"evidence,omitempty"`
}

// Facts are what the workflow needs to know about persisted data.
type Facts struct {
	HasOpenQuote     bool
	HasAcceptedQuote bool
	Now              time.Time
}

type InvalidTransitionError struct {
	From    State
	To      State
	Allowed []State
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("transition %s → %s is not allowed", e.From, e.To)
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type RequirementsError struct {
	From   State
	To     State
	Fields []FieldError
}

func (e *RequirementsError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return fmt.Sprintf("transition %s → %s is missing: %s", e.From, e.To, strings.Join(names, ", "))
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

const maxTextLen = 500

// Validate checks that from → in.To is allowed and that in carries the
// fields that transition needs. It returns *InvalidTransitionError or
// *RequirementsError.
func Validate(from State, in TransitionInput, f Facts) error {
	if !in.To.Valid() || !CanTransition(from, in.To) {
		return &InvalidTransitionError{From: from, To: in.To, Allowed: Allowed(from)}
	}
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }
	needReason := func(msg string) {
		if strings.TrimSpace(in.Reason) == "" {
			add("reason", msg)
		}
	}
	if utf8.RuneCountInString(in.Reason) > maxTextLen {
		add("reason", fmt.Sprintf("must be at most %d characters", maxTextLen))
	}

	switch in.To {
	case NeedsInfo:
		needReason("describe what information is missing")
	case New:
		needReason("describe what information was received")
	case Quoted:
		switch {
		case in.Quote == nil:
			add("quote", "a quote is required")
		default:
			if in.Quote.AmountCents <= 0 {
				add("quote.amount_cents", "must be greater than 0")
			}
			if _, err := currency.ParseISO(in.Quote.Currency); err != nil || !currencyRe.MatchString(in.Quote.Currency) {
				add("quote.currency", "must be an assigned ISO 4217 code")
			}
			if in.Quote.ValidUntil != nil && !in.Quote.ValidUntil.After(f.Now) {
				add("quote.valid_until", "must be in the future")
			}
		}
	case Accepted:
		if !f.HasOpenQuote {
			add("quote", "an open quote is required before acceptance")
		}
	case Scheduled:
		if !f.HasAcceptedQuote {
			add("quote", "an accepted quote is required before scheduling")
		}
		if in.PickupAt == nil {
			add("pickup_at", "pickup date and time is required")
		}
		if strings.TrimSpace(in.Carrier) == "" {
			add("carrier", "carrier is required")
		} else if utf8.RuneCountInString(in.Carrier) > 120 {
			add("carrier", "must be at most 120 characters")
		}
	case Delivered:
		switch {
		case in.Evidence == nil:
			add("evidence", "delivery evidence is required")
		default:
			if strings.TrimSpace(in.Evidence.ReceivedBy) == "" {
				add("evidence.received_by", "who received the delivery is required")
			}
			if in.Evidence.DeliveredAt == nil {
				add("evidence.delivered_at", "delivery time is required")
			} else if in.Evidence.DeliveredAt.After(f.Now.Add(5 * time.Minute)) {
				add("evidence.delivered_at", "cannot be in the future")
			}
			if utf8.RuneCountInString(in.Evidence.Note) > maxTextLen {
				add("evidence.note", fmt.Sprintf("must be at most %d characters", maxTextLen))
			}
		}
	case Cancelled:
		needReason("a cancellation reason is required")
	case Lost:
		needReason("a reason for losing the request is required")
	}
	if len(errs) > 0 {
		return &RequirementsError{From: from, To: in.To, Fields: errs}
	}
	return nil
}
