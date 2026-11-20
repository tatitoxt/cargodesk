// Package seed loads deterministic, clearly synthetic demo data. Every name,
// place and account below is fabricated; e-mails use the reserved .example TLD.
package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

// Fixed IDs keep demo links and tests stable across resets.
const (
	OrgA = "00000000-0000-4000-a000-000000000001"
	OrgB = "00000000-0000-4000-a000-000000000002"

	CoordinatorA = "00000000-0000-4000-b000-000000000001" // Ana Demo
	Coordinator2 = "00000000-0000-4000-b000-000000000002" // Bruno Sample
	AdminA       = "00000000-0000-4000-b000-000000000003" // Carla Admin
	ViewerA      = "00000000-0000-4000-b000-000000000004" // Dario Viewer
	CoordinatorB = "00000000-0000-4000-b000-000000000005" // Other-tenant coordinator
)

type org struct{ id, slug, name, tz, prefix string }

type user struct{ id, org, email, name, role string }

var orgs = []org{
	{OrgA, "demo-freight", "Demo Freight Co. (synthetic)", "America/Argentina/Buenos_Aires", "CD"},
	{OrgB, "sample-haulage", "Sample Haulage Ltd. (synthetic)", "UTC", "SH"},
}

var users = []user{
	{CoordinatorA, OrgA, "ana.coordinator@cargodesk.example", "Ana Demo", "coordinator"},
	{Coordinator2, OrgA, "bruno.coordinator@cargodesk.example", "Bruno Sample", "coordinator"},
	{AdminA, OrgA, "carla.admin@cargodesk.example", "Carla Admin", "admin"},
	{ViewerA, OrgA, "dario.viewer@cargodesk.example", "Dario Viewer", "viewer"},
	{CoordinatorB, OrgB, "erin.coordinator@sample-haulage.example", "Erin Other-Tenant", "coordinator"},
}

// Run inserts organizations, users, SLA policies and shipments in a range of
// states, built through the store so every row has a real audit trail.
// Timestamps are relative to now so overdue/at-risk examples stay current.
func Run(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	for _, o := range orgs {
		if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, slug, name, timezone, reference_prefix) VALUES ($1,$2,$3,$4,$5)`,
			o.id, o.slug, o.name, o.tz, o.prefix); err != nil {
			return fmt.Errorf("seed org %s: %w", o.slug, err)
		}
		for pr, h := range sla.DefaultPolicy() {
			if _, err := pool.Exec(ctx, `INSERT INTO sla_policies (organization_id, priority, hours) VALUES ($1,$2,$3)`, o.id, string(pr), h); err != nil {
				return fmt.Errorf("seed policy: %w", err)
			}
		}
	}
	for _, u := range users {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, organization_id, email, display_name, role) VALUES ($1,$2,$3,$4,$5)`,
			u.id, u.org, u.email, u.name, u.role); err != nil {
			return fmt.Errorf("seed user %s: %w", u.email, err)
		}
	}
	st := store.New(pool)
	ana, err := st.ActorByUserID(ctx, CoordinatorA)
	if err != nil {
		return err
	}
	erin, err := st.ActorByUserID(ctx, CoordinatorB)
	if err != nil {
		return err
	}
	return shipments(ctx, st, ana, erin, now.UTC().Truncate(time.Minute))
}

func ptr[T any](v T) *T { return &v }

type builder struct {
	ctx context.Context
	st  *store.Store
	a   store.Actor
	sh  store.Shipment
	at  time.Time
	err error
}

func (b *builder) step(d time.Duration) time.Time {
	b.at = b.at.Add(d)
	return b.at
}

func (b *builder) to(d time.Duration, in workflow.TransitionInput) *builder {
	if b.err != nil {
		return b
	}
	b.sh, b.err = b.st.Transition(b.ctx, b.a, b.sh.ID, b.sh.Version, in, b.step(d), "seed")
	return b
}

func (b *builder) assign(d time.Duration, in domain.AssignmentInput) *builder {
	if b.err != nil {
		return b
	}
	in.Version = b.sh.Version
	b.sh, b.err = b.st.Assign(b.ctx, b.a, b.sh.ID, in, b.step(d), "seed")
	return b
}

func (b *builder) note(d time.Duration, body string) *builder {
	if b.err != nil {
		return b
	}
	_, b.err = b.st.AddNote(b.ctx, b.a, b.sh.ID, body, b.step(d), "seed")
	return b
}

func create(ctx context.Context, st *store.Store, a store.Actor, at time.Time, in domain.CreateShipmentInput) *builder {
	in.Normalize()
	sh, err := st.CreateShipment(ctx, a, in, at, "seed")
	return &builder{ctx: ctx, st: st, a: a, sh: sh, at: at, err: err}
}

func shipments(ctx context.Context, st *store.Store, ana, erin store.Actor, now time.Time) error {
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	quote := func(c int64) *workflow.QuoteInput { return &workflow.QuoteInput{AmountCents: c, Currency: "USD"} }
	h := time.Hour
	bruno := ptr(Coordinator2)
	anaID := ptr(CoordinatorA)

	bs := []*builder{
		// Fresh, unassigned, on track.
		create(ctx, st, ana, now.Add(-1*h), domain.CreateShipmentInput{AccountName: "Acme Synthetic Foods", ContactLabel: "Demo buyer",
			Origin: "Warehouse North (demo)", Destination: "Depot East (demo)", CargoType: "palletized", RequiredDate: day(7),
			Notes: "12 pallets, tail-lift required."}),
		// Unassigned and past its quote SLA: shows up as overdue.
		create(ctx, st, ana, now.Add(-30*h), domain.CreateShipmentInput{AccountName: "Globex Sample Retail", ContactLabel: "Ops desk (demo)",
			Origin: "Port Terminal 3 (demo)", Destination: "Store Cluster B (demo)", CargoType: "general", RequiredDate: day(5),
			Priority: sla.High}),
		// Waiting for information from the customer.
		create(ctx, st, ana, now.Add(-5*h), domain.CreateShipmentInput{AccountName: "Initech Demo Parts", ContactLabel: "Purchasing (demo)",
			Origin: "Factory West (demo)", Destination: "Assembly Hub (demo)", CargoType: "bulk", RequiredDate: day(10)}).
			assign(10*time.Minute, domain.AssignmentInput{OwnerID: anaID}).
			to(20*time.Minute, workflow.TransitionInput{To: workflow.NeedsInfo, Reason: "Need cargo dimensions and weight."}).
			assign(time.Minute, domain.AssignmentInput{NextActionAt: ptr(now.Add(3 * h)), NextActionNote: ptr("Chase dimensions")}),
		// Quoted, waiting for acceptance.
		create(ctx, st, ana, now.Add(-20*h), domain.CreateShipmentInput{AccountName: "Umbrella Test Pharma", ContactLabel: "Logistics (demo)",
			Origin: "Cold Store 1 (demo)", Destination: "Clinic Network (demo)", CargoType: "refrigerated", RequiredDate: day(4), Priority: sla.Urgent}).
			assign(15*time.Minute, domain.AssignmentInput{OwnerID: bruno}).
			to(time.Hour, workflow.TransitionInput{To: workflow.Quoted, Quote: quote(185000)}).
			assign(time.Minute, domain.AssignmentInput{NextActionAt: ptr(now.Add(-2 * h)), NextActionNote: ptr("Call for quote approval")}).
			note(time.Minute, "Customer asked for a 2-hour delivery window."),
		// Scheduled with a carrier.
		create(ctx, st, ana, now.Add(-48*h), domain.CreateShipmentInput{AccountName: "Stark Fictional Industries", ContactLabel: "Shipping (demo)",
			Origin: "Plant 7 (demo)", Destination: "Distribution Center South (demo)", CargoType: "palletized", RequiredDate: day(2)}).
			assign(time.Hour, domain.AssignmentInput{OwnerID: anaID}).
			to(2*h, workflow.TransitionInput{To: workflow.Quoted, Quote: quote(92000)}).
			to(4*h, workflow.TransitionInput{To: workflow.Accepted}).
			to(h, workflow.TransitionInput{To: workflow.Scheduled, PickupAt: ptr(now.Add(20 * h)), Carrier: "Carrier Alpha (demo)"}),
		// In transit.
		create(ctx, st, ana, now.Add(-72*h), domain.CreateShipmentInput{AccountName: "Wayne Placeholder Logistics", ContactLabel: "Receiving (demo)",
			Origin: "Yard 2 (demo)", Destination: "Retail Park (demo)", CargoType: "general", RequiredDate: day(1)}).
			assign(time.Hour, domain.AssignmentInput{OwnerID: bruno}).
			to(3*h, workflow.TransitionInput{To: workflow.Quoted, Quote: quote(240000)}).
			to(5*h, workflow.TransitionInput{To: workflow.Accepted}).
			to(h, workflow.TransitionInput{To: workflow.Scheduled, PickupAt: ptr(now.Add(-6 * h)), Carrier: "Carrier Beta (demo)"}).
			to(40*h, workflow.TransitionInput{To: workflow.InTransit}),
		// Delivered with evidence.
		create(ctx, st, ana, now.Add(-120*h), domain.CreateShipmentInput{AccountName: "Acme Synthetic Foods", ContactLabel: "Demo buyer",
			Origin: "Warehouse North (demo)", Destination: "Depot West (demo)", CargoType: "palletized", RequiredDate: day(0)}).
			assign(time.Hour, domain.AssignmentInput{OwnerID: anaID}).
			to(2*h, workflow.TransitionInput{To: workflow.Quoted, Quote: quote(76000)}).
			to(3*h, workflow.TransitionInput{To: workflow.Accepted}).
			to(h, workflow.TransitionInput{To: workflow.Scheduled, PickupAt: ptr(now.Add(-60 * h)), Carrier: "Carrier Alpha (demo)"}).
			to(55*h, workflow.TransitionInput{To: workflow.InTransit}).
			to(8*h, workflow.TransitionInput{To: workflow.Delivered, Evidence: &workflow.EvidenceInput{
				ReceivedBy: "Gate clerk (demo)", DeliveredAt: ptr(now.Add(-52 * h)), Note: "Signed delivery note DN-0001 (synthetic)"}}),
		// Cancelled.
		create(ctx, st, ana, now.Add(-96*h), domain.CreateShipmentInput{AccountName: "Globex Sample Retail", ContactLabel: "Ops desk (demo)",
			Origin: "Port Terminal 1 (demo)", Destination: "Store Cluster A (demo)", CargoType: "bulk", RequiredDate: day(3)}).
			to(2*h, workflow.TransitionInput{To: workflow.Cancelled, Reason: "Customer postponed the order."}),
		// Another tenant: must never be visible to Demo Freight users.
		create(ctx, st, erin, now.Add(-2*h), domain.CreateShipmentInput{AccountName: "Hidden Tenant Account", ContactLabel: "Other tenant",
			Origin: "Other Origin (demo)", Destination: "Other Destination (demo)", CargoType: "oversized", RequiredDate: day(6)}),
	}
	for i, b := range bs {
		if b.err != nil {
			return fmt.Errorf("seed shipment %d: %w", i, b.err)
		}
	}
	return nil
}
