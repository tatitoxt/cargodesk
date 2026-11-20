package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/piiscan"
	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/testdb"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

var ctx = context.Background()

func setup(t *testing.T) (*store.Store, store.Actor, store.Actor) {
	pool := testdb.Fresh(t)
	st := store.New(pool)
	a, err := st.ActorByUserID(ctx, seed.CoordinatorA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ActorByUserID(ctx, seed.CoordinatorB)
	if err != nil {
		t.Fatal(err)
	}
	return st, a, b
}

func newShipment(t *testing.T, st *store.Store, a store.Actor) store.Shipment {
	t.Helper()
	in := domain.CreateShipmentInput{AccountName: "Test Account (synthetic)", ContactLabel: "Test contact", Origin: "A (demo)", Destination: "B (demo)",
		CargoType: "general", RequiredDate: "2026-03-20"}
	in.Normalize()
	sh, err := st.CreateShipment(ctx, a, in, testdb.Now, "test-create")
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func eventCount(t *testing.T, st *store.Store, a store.Actor, id string) int {
	t.Helper()
	evs, err := st.Events(ctx, a, id)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

func TestMigrationsAreIdempotent(t *testing.T) {
	st, _, _ := setup(t)
	applied, err := store.Migrate(ctx, st.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("second migrate applied %v", applied)
	}
	all, _ := store.Migrations()
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(all) {
		t.Fatalf("schema_migrations rows = %d (%v), want %d", n, err, len(all))
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	for _, q := range []string{
		`UPDATE shipment_events SET reason = 'tampered' WHERE shipment_id = $1`,
		`DELETE FROM shipment_events WHERE shipment_id = $1`,
	} {
		if _, err := st.Pool().Exec(ctx, q, sh.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: err = %v, want append-only violation", q, err)
		}
	}
	if _, err := st.Pool().Exec(ctx, `TRUNCATE shipment_events`); err == nil {
		t.Fatal("TRUNCATE succeeded")
	}
}

// A failed audit insert must roll back the state change it describes.
func TestTransitionAndAuditAreAtomic(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	tooLong := strings.Repeat("x", 101) // violates the correlation_id CHECK
	_, err := st.Transition(ctx, a, sh.ID, sh.Version, workflow.TransitionInput{To: workflow.Cancelled, Reason: "test"}, testdb.Now, tooLong)
	if err == nil {
		t.Fatal("expected audit insert failure")
	}
	d, err := st.GetShipment(ctx, a, sh.ID, testdb.Now)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != workflow.New || d.Version != sh.Version {
		t.Fatalf("state=%s version=%d after failed audit, want new/%d", d.State, d.Version, sh.Version)
	}
	if n := eventCount(t, st, a, sh.ID); n != 1 {
		t.Fatalf("events = %d, want 1 (created)", n)
	}
}

func TestVersionConflict(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	q := workflow.TransitionInput{To: workflow.Quoted, Quote: &workflow.QuoteInput{AmountCents: 1000, Currency: "USD"}}
	if _, err := st.Transition(ctx, a, sh.ID, sh.Version, q, testdb.Now, "first"); err != nil {
		t.Fatal(err)
	}
	_, err := st.Transition(ctx, a, sh.ID, sh.Version, workflow.TransitionInput{To: workflow.Cancelled, Reason: "stale"}, testdb.Now, "second")
	var vc *store.VersionConflictError
	if !errors.As(err, &vc) || vc.Current != sh.Version+1 {
		t.Fatalf("err = %v, want version conflict at %d", err, sh.Version+1)
	}
	if n := eventCount(t, st, a, sh.ID); n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
}

func TestTenantIsolation(t *testing.T) {
	st, a, b := setup(t)
	sh := newShipment(t, st, a)
	if _, err := st.GetShipment(ctx, b, sh.ID, testdb.Now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if _, err := st.Events(ctx, b, sh.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant events: %v", err)
	}
	if _, err := st.Transition(ctx, b, sh.ID, sh.Version, workflow.TransitionInput{To: workflow.Cancelled, Reason: "x"}, testdb.Now, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant transition: %v", err)
	}
	if _, err := st.AddNote(ctx, b, sh.ID, "hi", testdb.Now, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant note: %v", err)
	}
	foreign := seed.CoordinatorB
	_, err := st.Assign(ctx, a, sh.ID, domain.AssignmentInput{Version: sh.Version, OwnerID: &foreign}, testdb.Now, "x")
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("assigning another tenant's user: %v, want validation error", err)
	}
	list, err := st.ListShipments(ctx, b, store.ListFilter{}, testdb.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == sh.ID || !strings.HasPrefix(s.Reference, "SH-") {
			t.Fatalf("tenant B sees %s", s.Reference)
		}
	}
	users, _ := st.Users(ctx, b)
	for _, u := range users {
		if u.ID == seed.CoordinatorA {
			t.Fatal("tenant B lists tenant A users")
		}
	}
}

func TestReferencesArePerTenantSequences(t *testing.T) {
	st, a, b := setup(t)
	x, y := newShipment(t, st, a), newShipment(t, st, a)
	z := newShipment(t, st, b)
	if x.Reference == y.Reference || !strings.HasPrefix(x.Reference, "CD-") || !strings.HasPrefix(z.Reference, "SH-") {
		t.Fatalf("references %s %s %s", x.Reference, y.Reference, z.Reference)
	}
}

func TestFullLifecycleWritesTimeline(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	now := testdb.Now
	step := func(in workflow.TransitionInput) {
		t.Helper()
		now = now.Add(time.Hour)
		var err error
		if sh, err = st.Transition(ctx, a, sh.ID, sh.Version, in, now, "lifecycle"); err != nil {
			t.Fatalf("%s: %v", in.To, err)
		}
	}
	step(workflow.TransitionInput{To: workflow.NeedsInfo, Reason: "missing weight"})
	step(workflow.TransitionInput{To: workflow.New, Reason: "weight received"})
	step(workflow.TransitionInput{To: workflow.Quoted, Quote: &workflow.QuoteInput{AmountCents: 5000, Currency: "EUR"}})
	step(workflow.TransitionInput{To: workflow.NeedsInfo, Reason: "customer changed volume"})
	step(workflow.TransitionInput{To: workflow.Quoted, Quote: &workflow.QuoteInput{AmountCents: 6500, Currency: "EUR"}})
	step(workflow.TransitionInput{To: workflow.Accepted})
	pickup := now.Add(time.Hour)
	step(workflow.TransitionInput{To: workflow.Scheduled, PickupAt: &pickup, Carrier: "Carrier Test"})
	step(workflow.TransitionInput{To: workflow.InTransit})
	delivered := now.Add(30 * time.Minute)
	step(workflow.TransitionInput{To: workflow.Delivered, Evidence: &workflow.EvidenceInput{ReceivedBy: "Clerk", DeliveredAt: &delivered}})

	d, err := st.GetShipment(ctx, a, sh.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != workflow.Delivered || d.ReceivedBy == nil || *d.ReceivedBy != "Clerk" || d.NextActionAt != nil {
		t.Fatalf("final shipment %+v", d.Shipment)
	}
	statuses := map[string]int{}
	for _, q := range d.Quotes {
		statuses[q.Status]++
	}
	if statuses["accepted"] != 1 || statuses["superseded"] != 1 {
		t.Fatalf("quote statuses %v", statuses)
	}
	evs, _ := st.Events(ctx, a, sh.ID)
	if len(evs) != 10 {
		t.Fatalf("events = %d, want 10", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Action != "transitioned" || *last.FromState != "in_transit" || *last.ToState != "delivered" || last.CorrelationID != "lifecycle" || last.Actor.ID != a.UserID {
		t.Fatalf("last event %+v", last)
	}
	if _, err := st.Transition(ctx, a, sh.ID, d.Version, workflow.TransitionInput{To: workflow.Cancelled, Reason: "x"}, now, "x"); err == nil {
		t.Fatal("transition out of delivered succeeded")
	}
}

func TestPriorityChangeRecomputesDue(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	urgent := "urgent"
	got, err := st.Assign(ctx, a, sh.ID, domain.AssignmentInput{Version: sh.Version, Priority: (*sla.Priority)(&urgent)}, testdb.Now, "x")
	if err != nil {
		t.Fatal(err)
	}
	if want := sh.CreatedAt.Add(2 * time.Hour); !got.DueAt.Equal(want) {
		t.Fatalf("due = %v, want %v", got.DueAt, want)
	}
}

func TestExceptionLifecycle(t *testing.T) {
	st, a, _ := setup(t)
	sh := newShipment(t, st, a)
	follow := testdb.Now.Add(4 * time.Hour)
	sh, err := st.OpenException(ctx, a, sh.ID, domain.EventInput{Kind: domain.EventException, Version: sh.Version,
		ExceptionKind: "delay", Reason: "truck breakdown", FollowUpAt: &follow}, testdb.Now, "x")
	if err != nil {
		t.Fatal(err)
	}
	if sh.OpenExceptions != 1 || sh.NextActionAt == nil || !sh.NextActionAt.Equal(follow) {
		t.Fatalf("after open: %+v", sh)
	}
	d, _ := st.GetShipment(ctx, a, sh.ID, testdb.Now)
	if _, err := st.ResolveException(ctx, a, sh.ID, domain.EventInput{Kind: domain.EventExceptionResolved,
		ExceptionID: d.Exceptions[0].ID, Resolution: "replacement truck"}, testdb.Now, "x"); err != nil {
		t.Fatal(err)
	}
	d, _ = st.GetShipment(ctx, a, sh.ID, testdb.Now)
	if d.OpenExceptions != 0 {
		t.Fatalf("open exceptions = %d", d.OpenExceptions)
	}
	if _, err := st.ResolveException(ctx, a, sh.ID, domain.EventInput{Kind: domain.EventExceptionResolved,
		ExceptionID: d.Exceptions[0].ID, Resolution: "again"}, testdb.Now, "x"); err == nil {
		t.Fatal("resolving twice succeeded")
	}
}

func TestSessionsExpireAndAreHashed(t *testing.T) {
	st, _, _ := setup(t)
	tok, err := st.CreateSession(ctx, seed.ViewerA, testdb.Now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var raw int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, []byte(tok)).Scan(&raw)
	if raw != 0 {
		t.Fatal("raw token stored")
	}
	if _, err := st.ActorBySession(ctx, tok, testdb.Now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActorBySession(ctx, tok, testdb.Now.Add(2*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
}

// Seeded rows must pass the same PII/secret scan as the fixtures.
func TestSeedDataIsSynthetic(t *testing.T) {
	st, a, b := setup(t)
	var dump []any
	for _, act := range []store.Actor{a, b} {
		list, err := st.ListShipments(ctx, act, store.ListFilter{}, testdb.Now)
		if err != nil {
			t.Fatal(err)
		}
		users, _ := st.Users(ctx, act)
		dump = append(dump, list, users)
		for _, s := range list {
			evs, _ := st.Events(ctx, act, s.ID)
			dump = append(dump, evs)
		}
	}
	js, _ := json.MarshalIndent(dump, "", " ")
	if f := piiscan.Scan("seed", js); len(f) > 0 {
		t.Fatalf("seed data findings: %+v", f)
	}
}

func TestStaleIdempotencyReservationCannotOverwriteReplacement(t *testing.T) {
	st, a, _ := setup(t)
	oldHash, newHash := store.RequestHash("s", []byte("old")), store.RequestHash("s", []byte("new"))
	if prev, err := st.ReserveIdempotent(ctx, a, "k-stale", "s", oldHash, testdb.Now); prev != nil || err != nil {
		t.Fatalf("reserve old: %v %v", prev, err)
	}
	later := testdb.Now.Add(2 * time.Minute)
	if prev, err := st.ReserveIdempotent(ctx, a, "k-stale", "s", newHash, later); prev != nil || err != nil {
		t.Fatalf("reserve replacement: %v %v", prev, err)
	}
	// The original request finishes late; it must not touch the replacement.
	if err := st.CompleteIdempotent(ctx, a, "k-stale", oldHash, 201, []byte(`{"id":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveIdempotent(ctx, a, "k-stale", "s", newHash, later); !errors.Is(err, store.ErrIdempotencyInProgress) {
		t.Fatalf("replacement should still be pending, got %v", err)
	}
	if err := st.CompleteIdempotent(ctx, a, "k-stale", newHash, 201, []byte(`{"id":"new"}`)); err != nil {
		t.Fatal(err)
	}
	prev, err := st.ReserveIdempotent(ctx, a, "k-stale", "s", newHash, later)
	if err != nil || prev == nil || string(prev.Body) != `{"id": "new"}` {
		t.Fatalf("replay = %v %v", prev, err)
	}
}
