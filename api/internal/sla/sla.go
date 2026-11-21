// Package sla computes internal "time to quote" due dates, SLA status,
// overdue work and "due today" in the organization's operating timezone.
package sla

import (
	"fmt"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

type Priority string

const (
	Low    Priority = "low"
	Normal Priority = "normal"
	High   Priority = "high"
	Urgent Priority = "urgent"
)

var Priorities = []Priority{Low, Normal, High, Urgent}

func (p Priority) Valid() bool {
	switch p {
	case Low, Normal, High, Urgent:
		return true
	}
	return false
}

// Policy maps each priority to the hours allowed until a quote is sent.
type Policy map[Priority]int

func DefaultPolicy() Policy { return Policy{Low: 72, Normal: 24, High: 8, Urgent: 2} }

const maxHours = 24 * 30

func (p Policy) Validate() error {
	for _, pr := range Priorities {
		h, ok := p[pr]
		if !ok {
			return fmt.Errorf("missing hours for priority %q", pr)
		}
		if h < 1 || h > maxHours {
			return fmt.Errorf("hours for %q must be between 1 and %d", pr, maxHours)
		}
	}
	if len(p) != len(Priorities) {
		return fmt.Errorf("unknown priority in policy")
	}
	return nil
}

// DueAt is when the quote is due for a request created at createdAt.
func DueAt(createdAt time.Time, pr Priority, p Policy) time.Time {
	return createdAt.Add(time.Duration(p[pr]) * time.Hour).UTC()
}

type Status string

const (
	OnTrack    Status = "on_track"
	AtRisk     Status = "at_risk"
	Breached   Status = "breached"
	Met        Status = "met"
	Missed     Status = "missed"
	NotApplied Status = "not_applicable"
)

// atRiskWindow is how close to due_at an unquoted request turns "at_risk".
const atRiskWindow = 2 * time.Hour

// StatusOf reports the SLA status. quotedAt is when the shipment first
// reached "quoted" (nil if never).
func StatusOf(state workflow.State, dueAt time.Time, quotedAt *time.Time, now time.Time) Status {
	if quotedAt != nil {
		if quotedAt.After(dueAt) {
			return Missed
		}
		return Met
	}
	if !state.AwaitingQuote() {
		return NotApplied
	}
	switch {
	case !now.Before(dueAt):
		return Breached
	case dueAt.Sub(now) <= atRiskWindow:
		return AtRisk
	default:
		return OnTrack
	}
}

// Overdue reports whether a shipment needs attention now: it is not terminal
// and either its next action is due or its quote SLA is breached. The SQL
// filter in the store package must match this definition.
func Overdue(state workflow.State, nextActionAt *time.Time, dueAt time.Time, now time.Time) bool {
	if state.Terminal() {
		return false
	}
	if nextActionAt != nil && !nextActionAt.After(now) {
		return true
	}
	return state.AwaitingQuote() && !dueAt.After(now)
}

// DayBounds returns the UTC instants [start, end) of the calendar day that
// contains now in loc.
func DayBounds(now time.Time, loc *time.Location) (time.Time, time.Time) {
	l := now.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	return start.UTC(), end.UTC()
}

// DueToday reports whether t falls on today's date in loc.
func DueToday(t *time.Time, now time.Time, loc *time.Location) bool {
	if t == nil {
		return false
	}
	start, end := DayBounds(now, loc)
	return !t.Before(start) && t.Before(end)
}

// Calendar describes which minutes of a week count toward an SLA, in the
// organization's timezone (days are ISO weekdays: 1 = Monday .. 7 = Sunday).
type Calendar struct {
	Days        []int    `json:"days"`
	StartMinute int      `json:"start_minute"`
	EndMinute   int      `json:"end_minute"`
	Holidays    []string `json:"holidays"` // YYYY-MM-DD, in the org timezone
}

func (c Calendar) Validate() error {
	seen := map[int]bool{}
	for _, d := range c.Days {
		if d < 1 || d > 7 || seen[d] {
			return fmt.Errorf("days must be unique ISO weekdays 1-7, got %d", d)
		}
		seen[d] = true
	}
	if len(c.Days) == 0 {
		return fmt.Errorf("days must name at least one working day")
	}
	if c.StartMinute < 0 || c.EndMinute > 1440 || c.StartMinute >= c.EndMinute {
		return fmt.Errorf("start_minute/end_minute must satisfy 0 <= start < end <= 1440")
	}
	for _, h := range c.Holidays {
		if _, err := time.Parse("2006-01-02", h); err != nil {
			return fmt.Errorf("holiday %q is not YYYY-MM-DD", h)
		}
	}
	return nil
}

func (c Calendar) working(d time.Time) bool {
	if !contains(c.Days, isoWeekday(d)) {
		return false
	}
	for _, h := range c.Holidays {
		if d.Format("2006-01-02") == h {
			return false
		}
	}
	return true
}

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// DueAtBusiness is DueAt measured in business minutes instead of wall-clock
// hours: the clock runs only inside the calendar's daily windows on working,
// non-holiday days, evaluated in loc. createdAt instant is preserved (no
// rounding); the result is UTC.
func DueAtBusiness(createdAt time.Time, hours int, c Calendar, loc *time.Location) time.Time {
	remaining := hours * 60
	cur := createdAt.In(loc)
	// Each iteration skips at least one calendar day or consumes a full window.
	for i := 0; i < 4*366; i++ {
		dayStart := time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, loc)
		if !c.working(cur) {
			cur = dayStart.AddDate(0, 0, 1)
			continue
		}
		winStart := dayStart.Add(time.Duration(c.StartMinute) * time.Minute)
		winEnd := dayStart.Add(time.Duration(c.EndMinute) * time.Minute)
		if !cur.Before(winEnd) {
			cur = dayStart.AddDate(0, 0, 1)
			continue
		}
		if cur.Before(winStart) {
			cur = winStart
		}
		left := int(winEnd.Sub(cur).Minutes())
		if left >= remaining {
			return cur.Add(time.Duration(remaining) * time.Minute).UTC()
		}
		remaining -= left
		cur = winEnd
	}
	return createdAt.UTC()
}
