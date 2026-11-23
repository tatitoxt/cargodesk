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

// Policy maps each priority to the hours allowed for a stage.
type Policy map[Priority]int

// Stage names the SLA window a policy applies to.
type Stage string

const (
	StageQuote    Stage = "quote"    // created -> quoted
	StageSchedule Stage = "schedule" // accepted -> scheduled
	StageTransit  Stage = "transit"  // in transit -> delivered
)

var Stages = []Stage{StageQuote, StageSchedule, StageTransit}

// DefaultPolicy is the default time-to-quote policy.
func DefaultPolicy() Policy { return Policy{Low: 72, Normal: 24, High: 8, Urgent: 2} }

// DefaultSchedulePolicy is the default time-to-schedule policy (accepted to
// pickup scheduled) — looser than quoting but tighter than never.
func DefaultSchedulePolicy() Policy { return Policy{Low: 96, Normal: 48, High: 24, Urgent: 8} }

// DefaultTransitPolicy is the default time-in-transit policy (pickup to
// delivered) — the loosest stage, freight timescales.
func DefaultTransitPolicy() Policy { return Policy{Low: 168, Normal: 96, High: 48, Urgent: 24} }

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
// and either its next action is due, its quote SLA is breached or — while
// waiting to be scheduled — its schedule SLA is breached. The SQL filter in
// the store package must match this definition.
func Overdue(state workflow.State, nextActionAt *time.Time, dueAt time.Time, scheduleDueAt, transitDueAt *time.Time, now time.Time) bool {
	if state.Terminal() {
		return false
	}
	if nextActionAt != nil && !nextActionAt.After(now) {
		return true
	}
	if state.AwaitingQuote() && !dueAt.After(now) {
		return true
	}
	if state == workflow.Accepted && scheduleDueAt != nil && !scheduleDueAt.After(now) {
		return true
	}
	return state == workflow.InTransit && transitDueAt != nil && !transitDueAt.After(now)
}

// StatusOfSchedule reports the schedule-stage SLA status. scheduleDueAt is nil
// for shipments accepted before the stage SLA existed (not applicable).
// scheduledAt is when the shipment first reached "scheduled".
func StatusOfSchedule(state workflow.State, scheduleDueAt, scheduledAt *time.Time, now time.Time) Status {
	if scheduledAt != nil {
		if scheduleDueAt != nil && scheduledAt.After(*scheduleDueAt) {
			return Missed
		}
		return Met
	}
	if state != workflow.Accepted || scheduleDueAt == nil {
		return NotApplied
	}
	switch {
	case !now.Before(*scheduleDueAt):
		return Breached
	case scheduleDueAt.Sub(now) <= atRiskWindow:
		return AtRisk
	default:
		return OnTrack
	}
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
	Days        []int `json:"days"`
	StartMinute int   `json:"start_minute"`
	EndMinute   int   `json:"end_minute"`
	// Windows optionally gives each weekday its own start/end minutes — e.g.
	// a Saturday half-day. When non-empty it overrides days/start_minute/
	// end_minute; days absent from windows are non-working.
	Windows  []DayWindow `json:"windows,omitempty"`
	Holidays []string    `json:"holidays"` // YYYY-MM-DD, in the org timezone
}

// DayWindow is one weekday's working window, in minutes after local midnight.
type DayWindow struct {
	Day         int `json:"day"`
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

func (c Calendar) Validate() error {
	seen := map[int]bool{}
	for _, d := range c.Days {
		if d < 1 || d > 7 || seen[d] {
			return fmt.Errorf("days must be unique ISO weekdays 1-7, got %d", d)
		}
		seen[d] = true
	}
	if len(c.Windows) > 0 {
		seen = map[int]bool{}
		for _, w := range c.Windows {
			if w.Day < 1 || w.Day > 7 || seen[w.Day] {
				return fmt.Errorf("windows must name unique ISO weekdays 1-7, got %d", w.Day)
			}
			seen[w.Day] = true
			if w.StartMinute < 0 || w.EndMinute > 1440 || w.StartMinute >= w.EndMinute {
				return fmt.Errorf("window minutes must satisfy 0 <= start < end <= 1440")
			}
		}
	} else {
		if len(c.Days) == 0 {
			return fmt.Errorf("days must name at least one working day")
		}
		if c.StartMinute < 0 || c.EndMinute > 1440 || c.StartMinute >= c.EndMinute {
			return fmt.Errorf("start_minute/end_minute must satisfy 0 <= start < end <= 1440")
		}
	}
	for _, h := range c.Holidays {
		if _, err := time.Parse("2006-01-02", h); err != nil {
			return fmt.Errorf("holiday %q is not YYYY-MM-DD", h)
		}
	}
	return nil
}

// window returns the working window for d's ISO weekday, or false when d is
// a non-working day (per-day windows take priority over the shared one).
func (c Calendar) window(d time.Time) (int, int, bool) {
	wd := isoWeekday(d)
	if len(c.Windows) > 0 {
		for _, w := range c.Windows {
			if w.Day == wd {
				return w.StartMinute, w.EndMinute, true
			}
		}
		return 0, 0, false
	}
	if !contains(c.Days, wd) {
		return 0, 0, false
	}
	return c.StartMinute, c.EndMinute, true
}

func (c Calendar) working(d time.Time) bool {
	if _, _, ok := c.window(d); !ok {
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
	remaining := time.Duration(hours) * time.Hour
	cur := createdAt.In(loc)
	// Each iteration skips one calendar day or consumes the rest of a window;
	// the bound covers a 720 h allowance over the narrowest valid window.
	for i := 0; i < 200_000; i++ {
		y, m, d := cur.Date()
		next := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		if !c.working(cur) {
			cur = next
			continue
		}
		startMin, endMin, _ := c.window(cur)
		// Build wall-clock times directly so DST transitions keep 09:00 at 09:00.
		winStart := time.Date(y, m, d, startMin/60, startMin%60, 0, 0, loc)
		winEnd := time.Date(y, m, d, endMin/60, endMin%60, 0, 0, loc)
		if !cur.Before(winEnd) {
			cur = next
			continue
		}
		if cur.Before(winStart) {
			cur = winStart
		}
		left := winEnd.Sub(cur)
		if left >= remaining {
			return cur.Add(remaining).UTC()
		}
		remaining -= left
		cur = winEnd
	}
	return createdAt.UTC()
}

// StatusOfTransit reports the transit-stage SLA status. transitDueAt is nil
// for shipments that entered transit before the stage existed; deliveredAt is
// when the shipment was delivered.
func StatusOfTransit(state workflow.State, transitDueAt, deliveredAt *time.Time, now time.Time) Status {
	if deliveredAt != nil {
		if transitDueAt != nil && deliveredAt.After(*transitDueAt) {
			return Missed
		}
		return Met
	}
	if state != workflow.InTransit || transitDueAt == nil {
		return NotApplied
	}
	switch {
	case !now.Before(*transitDueAt):
		return Breached
	case transitDueAt.Sub(now) <= atRiskWindow:
		return AtRisk
	default:
		return OnTrack
	}
}
