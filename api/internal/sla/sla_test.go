package sla

import (
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

var now = time.Date(2026, 3, 11, 2, 30, 0, 0, time.UTC) // 2026-03-10 23:30 in Buenos Aires

func ptr(t time.Time) *time.Time { return &t }

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestDueAtUsesPolicyHours(t *testing.T) {
	created := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()
	if got := DueAt(created, Urgent, p); !got.Equal(created.Add(2 * time.Hour)) {
		t.Fatalf("urgent due = %v", got)
	}
	p[Normal] = 36
	if got := DueAt(created, Normal, p); !got.Equal(created.Add(36 * time.Hour)) {
		t.Fatalf("normal due = %v", got)
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := DefaultPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Policy{
		{Low: 72, Normal: 24, High: 8},
		{Low: 72, Normal: 0, High: 8, Urgent: 2},
		{Low: 72, Normal: 24, High: 8, Urgent: maxHours + 1},
		{Low: 72, Normal: 24, High: 8, Urgent: 2, "critical": 1},
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}

func TestStatusOf(t *testing.T) {
	due := now.Add(time.Hour)
	cases := []struct {
		name   string
		state  workflow.State
		due    time.Time
		quoted *time.Time
		want   Status
	}{
		{"on track", workflow.New, now.Add(5 * time.Hour), nil, OnTrack},
		{"at risk inside 2h", workflow.NeedsInfo, due, nil, AtRisk},
		{"breached at due instant", workflow.New, now, nil, Breached},
		{"met", workflow.Quoted, due, ptr(now), Met},
		{"missed", workflow.Accepted, now.Add(-time.Hour), ptr(now), Missed},
		{"cancelled before quote", workflow.Cancelled, now.Add(-time.Hour), nil, NotApplied},
	}
	for _, c := range cases {
		if got := StatusOf(c.state, c.due, c.quoted, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestOverdue(t *testing.T) {
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	cases := []struct {
		name  string
		state workflow.State
		next  *time.Time
		due   time.Time
		sdue  *time.Time
		tdue  *time.Time
		want  bool
	}{
		{"next action passed", workflow.Scheduled, &past, future, nil, nil, true},
		{"next action exactly now", workflow.Quoted, ptr(now), future, nil, nil, true},
		{"next action in future, sla ok", workflow.New, &future, future, nil, nil, false},
		{"sla breached while awaiting quote", workflow.NeedsInfo, nil, past, nil, nil, true},
		{"sla breach ignored after quote", workflow.Quoted, nil, past, nil, nil, false},
		{"terminal never overdue", workflow.Delivered, &past, past, nil, nil, false},
		{"cancelled never overdue", workflow.Cancelled, &past, past, nil, nil, false},
		{"schedule sla breached while accepted", workflow.Accepted, nil, future, &past, nil, true},
		{"schedule sla pending while accepted", workflow.Accepted, nil, future, &future, nil, false},
		{"no schedule sla for legacy rows", workflow.Accepted, nil, future, nil, nil, false},
		{"schedule sla ignored in other states", workflow.InTransit, nil, future, &past, nil, false},
		{"transit sla breached in transit", workflow.InTransit, nil, future, nil, &past, true},
		{"transit sla pending in transit", workflow.InTransit, nil, future, nil, &future, false},
		{"transit sla ignored in other states", workflow.Scheduled, nil, future, nil, &past, false},
	}
	for _, c := range cases {
		if got := Overdue(c.state, c.next, c.due, c.sdue, c.tdue, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatusOfSchedule(t *testing.T) {
	past, soon, future := now.Add(-time.Minute), now.Add(time.Hour), now.Add(48*time.Hour)
	cases := []struct {
		name      string
		state     workflow.State
		due, done *time.Time
		want      Status
	}{
		{"met before due", workflow.Scheduled, &future, &past, Met},
		{"missed after due", workflow.Scheduled, &past, &now, Missed},
		{"breached while accepted", workflow.Accepted, &past, nil, Breached},
		{"at risk within window", workflow.Accepted, &soon, nil, AtRisk},
		{"on track with room", workflow.Accepted, &future, nil, OnTrack},
		{"not applicable before accepted", workflow.Quoted, &future, nil, NotApplied},
		{"not applicable without due", workflow.Accepted, nil, nil, NotApplied},
	}
	for _, c := range cases {
		if got := StatusOfSchedule(c.state, c.due, c.done, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDueTodayDependsOnTimezone(t *testing.T) {
	ba := mustLoc(t, "America/Argentina/Buenos_Aires")
	// 22:00 on 2026-03-10 in Buenos Aires, 01:00 on 2026-03-11 in UTC.
	next := time.Date(2026, 3, 11, 1, 0, 0, 0, time.UTC)
	if !DueToday(&next, now, ba) {
		t.Error("should be due today in Buenos Aires")
	}
	morning := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	if DueToday(&morning, now, time.UTC) {
		t.Error("2026-03-10 is yesterday in UTC")
	}
	if !DueToday(&morning, now, ba) {
		t.Error("2026-03-10 07:00 local is today in Buenos Aires")
	}
	if DueToday(nil, now, ba) {
		t.Error("nil is never due")
	}
}

func TestDayBoundsAcrossDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08 is the US spring-forward day: 23 hours long.
	start, end := DayBounds(time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC), ny)
	if end.Sub(start) != 23*time.Hour {
		t.Fatalf("DST day length = %v", end.Sub(start))
	}
	if start != time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC) {
		t.Fatalf("start = %v", start)
	}
}

func TestPriorityValid(t *testing.T) {
	for _, p := range Priorities {
		if !p.Valid() {
			t.Errorf("%s should be valid", p)
		}
	}
	if Priority("critical").Valid() {
		t.Error("critical is not a priority")
	}
}

func TestCalendarValidate(t *testing.T) {
	ok := Calendar{Days: []int{1, 2, 3, 4, 5}, StartMinute: 540, EndMinute: 1020}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Calendar{
		{Days: nil, StartMinute: 0, EndMinute: 60},
		{Days: []int{0}, StartMinute: 0, EndMinute: 60},
		{Days: []int{8}, StartMinute: 0, EndMinute: 60},
		{Days: []int{1, 1}, StartMinute: 0, EndMinute: 60},
		{Days: []int{1}, StartMinute: 60, EndMinute: 60},
		{Days: []int{1}, StartMinute: 0, EndMinute: 1441},
		{Days: []int{1}, StartMinute: 0, EndMinute: 60, Holidays: []string{"2026/03/10"}},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("%+v should not validate", bad)
		}
	}
}

// Mon-Fri 09:00-17:00 in UTC.
var weekdays9to5 = Calendar{Days: []int{1, 2, 3, 4, 5}, StartMinute: 540, EndMinute: 1020}

func TestDueAtBusinessInsideWindow(t *testing.T) {
	// Monday 2026-03-09 10:00 UTC + 2h => same day 12:00.
	created := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	got := DueAtBusiness(created, 2, weekdays9to5, time.UTC)
	if want := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestDueAtBusinessBeforeWindowStartsToday(t *testing.T) {
	created := time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC) // before 09:00
	got := DueAtBusiness(created, 1, weekdays9to5, time.UTC)
	if want := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestDueAtBusinessRollsToNextDay(t *testing.T) {
	created := time.Date(2026, 3, 9, 16, 0, 0, 0, time.UTC) // 1h left today
	got := DueAtBusiness(created, 2, weekdays9to5, time.UTC)
	if want := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestDueAtBusinessSkipsWeekend(t *testing.T) {
	created := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // Friday 16:00
	got := DueAtBusiness(created, 2, weekdays9to5, time.UTC)
	if want := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC); !got.Equal(want) { // Monday 10:00
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestDueAtBusinessSkipsHoliday(t *testing.T) {
	cal := weekdays9to5
	cal.Holidays = []string{"2026-03-10"}
	created := time.Date(2026, 3, 9, 16, 0, 0, 0, time.UTC)
	got := DueAtBusiness(created, 2, cal, time.UTC)
	if want := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC); !got.Equal(want) { // Wednesday
		t.Fatalf("due = %v, want %v", got, want)
	}
}

// Mon-Fri 09:00-17:00 plus a Saturday half-day 09:00-13:00, expressed as
// per-day windows.
var weekdays9to5SatHalf = Calendar{Windows: []DayWindow{
	{Day: 1, StartMinute: 540, EndMinute: 1020},
	{Day: 2, StartMinute: 540, EndMinute: 1020},
	{Day: 3, StartMinute: 540, EndMinute: 1020},
	{Day: 4, StartMinute: 540, EndMinute: 1020},
	{Day: 5, StartMinute: 540, EndMinute: 1020},
	{Day: 6, StartMinute: 540, EndMinute: 780},
}}

func TestDueAtBusinessSaturdayHalfDay(t *testing.T) {
	// Friday 16:00 + 2h: 1h Friday, then 3h Saturday (09:00-12:00) is enough.
	created := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC)
	got := DueAtBusiness(created, 2, weekdays9to5SatHalf, time.UTC)
	if want := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
	// Friday 16:00 + 5h: 1h Friday, then exactly the 4h Saturday window —
	// due at Saturday window close.
	got = DueAtBusiness(created, 5, weekdays9to5SatHalf, time.UTC)
	if want := time.Date(2026, 3, 14, 13, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestDueAtBusinessWindowsOverrideSharedWindow(t *testing.T) {
	// Days says Mon-Fri, but Windows drops Saturday only — Windows wins, and
	// a day missing from windows is non-working even if in days.
	cal := Calendar{
		Days:        []int{1, 2, 3, 4, 5, 6, 7},
		StartMinute: 0,
		EndMinute:   1440,
		Windows: []DayWindow{
			{Day: 1, StartMinute: 540, EndMinute: 1020},
		},
	}
	created := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // Friday: not in windows
	got := DueAtBusiness(created, 1, cal, time.UTC)
	if want := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
}

func TestCalendarValidateWindows(t *testing.T) {
	for _, c := range []Calendar{
		{Windows: []DayWindow{{Day: 0, StartMinute: 0, EndMinute: 60}}},
		{Windows: []DayWindow{{Day: 8, StartMinute: 0, EndMinute: 60}}},
		{Windows: []DayWindow{{Day: 1, StartMinute: 60, EndMinute: 60}}},
		{Windows: []DayWindow{{Day: 1, StartMinute: 0, EndMinute: 1441}}},
		{Windows: []DayWindow{{Day: 1, StartMinute: 0, EndMinute: 60}, {Day: 1, StartMinute: 0, EndMinute: 60}}},
	} {
		if err := c.Validate(); err == nil {
			t.Fatalf("expected invalid calendar: %+v", c)
		}
	}
}
