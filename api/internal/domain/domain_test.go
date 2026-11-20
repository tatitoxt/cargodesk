package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/sla"
)

var (
	now = time.Date(2026, 3, 11, 2, 30, 0, 0, time.UTC) // 2026-03-10 23:30 in Buenos Aires
	ba  *time.Location
)

func init() {
	var err error
	if ba, err = time.LoadLocation("America/Argentina/Buenos_Aires"); err != nil {
		panic(err)
	}
}

func fields(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	var out []string
	for _, f := range ve.Fields {
		out = append(out, f.Field)
	}
	return out
}

func validCreate() CreateShipmentInput {
	return CreateShipmentInput{
		AccountName: "Northwind Demo Freight (fictitious)", ContactLabel: "Dispatch desk",
		Origin: "Warehouse 4, Example Port", Destination: "Depot B, Sample City",
		CargoType: "palletized", RequiredDate: "2026-03-12", Priority: sla.Normal,
	}
}

func TestCreateValid(t *testing.T) {
	in := validCreate()
	in.Normalize()
	if in.Priority != sla.Normal {
		t.Fatalf("default priority = %s", in.Priority)
	}
	if err := in.Validate(now, ba); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRequiredFields(t *testing.T) {
	in := CreateShipmentInput{AccountName: "   "}
	in.Normalize()
	got := fields(t, in.Validate(now, ba))
	want := []string{"account_name", "contact_label", "origin", "destination", "cargo_type", "required_date"}
	if !slices.Equal(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestCreateInvalidDates(t *testing.T) {
	for _, d := range []string{"2026-02-30", "10/03/2026", "2026-3-12", "tomorrow"} {
		in := validCreate()
		in.RequiredDate = d
		if got := fields(t, in.Validate(now, ba)); !slices.Equal(got, []string{"required_date"}) {
			t.Errorf("%q: fields = %v", d, got)
		}
	}
}

func TestRequiredDateUsesOperatingTimezone(t *testing.T) {
	in := validCreate()
	in.RequiredDate = "2026-03-10" // still today in Buenos Aires, yesterday in UTC
	if err := in.Validate(now, ba); err != nil {
		t.Fatalf("today in Buenos Aires rejected: %v", err)
	}
	if got := fields(t, in.Validate(now, time.UTC)); !slices.Equal(got, []string{"required_date"}) {
		t.Fatalf("past date in UTC accepted: %v", got)
	}
}

func TestCreateEnumsAndLengths(t *testing.T) {
	in := validCreate()
	in.CargoType = "livestock"
	in.Priority = "critical"
	bad := "not-a-uuid"
	in.OwnerID = &bad
	in.Notes = strings.Repeat("n", 2001)
	got := fields(t, in.Validate(now, ba))
	want := []string{"notes", "cargo_type", "priority", "owner_id"}
	if !slices.Equal(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestAssignmentValidate(t *testing.T) {
	if got := fields(t, AssignmentInput{}.Validate()); !slices.Equal(got, []string{"version", "assignment"}) {
		t.Fatalf("empty: %v", got)
	}
	p := sla.Priority("x")
	next := now
	got := fields(t, AssignmentInput{Version: 1, Priority: &p, NextActionAt: &next, ClearNext: true}.Validate())
	if !slices.Equal(got, []string{"priority", "next_action_at"}) {
		t.Fatalf("got %v", got)
	}
	empty := ""
	if err := (AssignmentInput{Version: 2, OwnerID: &empty}).Validate(); err != nil {
		t.Fatalf("clearing owner should be valid: %v", err)
	}
}

func TestEventValidate(t *testing.T) {
	cases := []struct {
		in   EventInput
		want []string
	}{
		{EventInput{Kind: "note", Body: "Called the dock"}, nil},
		{EventInput{Kind: "note"}, []string{"body"}},
		{EventInput{Kind: "exception"}, []string{"version", "exception_kind", "reason", "follow_up_at"}},
		{EventInput{Kind: "exception", Version: 1, ExceptionKind: "delay", Reason: "Truck late", FollowUpAt: &now}, nil},
		{EventInput{Kind: "exception_resolved", ExceptionID: "x"}, []string{"exception_id", "resolution"}},
		{EventInput{Kind: "edit_state"}, []string{"kind"}},
	}
	for i, c := range cases {
		if got := fields(t, c.in.Validate()); !slices.Equal(got, c.want) {
			t.Errorf("case %d: fields = %v, want %v", i, got, c.want)
		}
	}
}

func TestRoles(t *testing.T) {
	if Viewer.CanEdit() || Viewer.CanExport() || Viewer.CanConfigure() {
		t.Error("viewer must be read-only")
	}
	if !Coordinator.CanEdit() || !Coordinator.CanExport() || Coordinator.CanConfigure() {
		t.Error("coordinator permissions wrong")
	}
	if !Admin.CanEdit() || !Admin.CanExport() || !Admin.CanConfigure() {
		t.Error("admin permissions wrong")
	}
}

func TestIsUUID(t *testing.T) {
	if !IsUUID("0b8f3c6e-6d1a-4c7e-9a51-2f0d4e7c9b10") {
		t.Error("valid uuid rejected")
	}
	for _, s := range []string{"", "0b8f3c6e6d1a4c7e9a512f0d4e7c9b10", "0b8f3c6e-6d1a-4c7e-9a51-2f0d4e7c9bzz"} {
		if IsUUID(s) {
			t.Errorf("%q accepted", s)
		}
	}
}
