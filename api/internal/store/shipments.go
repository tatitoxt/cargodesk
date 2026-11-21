package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
)

type Shipment struct {
	ID             string         `json:"id"`
	Reference      string         `json:"reference"`
	AccountName    string         `json:"account_name"`
	ContactLabel   string         `json:"contact_label"`
	Origin         string         `json:"origin"`
	Destination    string         `json:"destination"`
	CargoType      string         `json:"cargo_type"`
	RequiredDate   string         `json:"required_date"`
	Notes          string         `json:"notes"`
	State          workflow.State `json:"state"`
	Priority       sla.Priority   `json:"priority"`
	Owner          *UserRef       `json:"owner"`
	NextActionAt   *time.Time     `json:"next_action_at"`
	NextActionNote string         `json:"next_action_note"`
	DueAt          time.Time      `json:"due_at"`
	QuotedAt       *time.Time     `json:"quoted_at"`
	AcceptedAt     *time.Time     `json:"accepted_at"`
	ScheduledAt    *time.Time     `json:"scheduled_at"`
	ScheduleDueAt  *time.Time     `json:"schedule_due_at"`
	PickupAt       *time.Time     `json:"pickup_at"`
	Carrier        *string        `json:"carrier"`
	DeliveredAt    *time.Time     `json:"delivered_at"`
	ReceivedBy     *string        `json:"received_by"`
	DeliveryNote   *string        `json:"delivery_note"`
	ClosedReason   *string        `json:"closed_reason"`
	OpenExceptions int            `json:"open_exceptions"`
	Version        int            `json:"version"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`

	// Derived at read time from the clock and the operating timezone.
	SLAStatus          sla.Status       `json:"sla_status"`
	ScheduleSLAStatus  sla.Status       `json:"schedule_sla_status"`
	Overdue            bool             `json:"overdue"`
	DueToday           bool             `json:"due_today"`
	AllowedTransitions []workflow.State `json:"allowed_transitions"`
}

func (sh *Shipment) derive(now time.Time, loc *time.Location) {
	sh.SLAStatus = sla.StatusOf(sh.State, sh.DueAt, sh.QuotedAt, now)
	sh.ScheduleSLAStatus = sla.StatusOfSchedule(sh.State, sh.ScheduleDueAt, sh.ScheduledAt, now)
	sh.Overdue = sla.Overdue(sh.State, sh.NextActionAt, sh.DueAt, sh.ScheduleDueAt, now)
	sh.DueToday = sla.DueToday(sh.NextActionAt, now, loc)
	sh.AllowedTransitions = workflow.Allowed(sh.State)
}

type Quote struct {
	ID          string     `json:"id"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	ValidUntil  *time.Time `json:"valid_until"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Exception struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Reason     string     `json:"reason"`
	Owner      *UserRef   `json:"owner"`
	FollowUpAt time.Time  `json:"follow_up_at"`
	OpenedAt   time.Time  `json:"opened_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	Resolution *string    `json:"resolution"`
}

type Event struct {
	ID            int64          `json:"id"`
	Action        string         `json:"action"`
	Actor         UserRef        `json:"actor"`
	FromState     *string        `json:"from_state"`
	ToState       *string        `json:"to_state"`
	Reason        *string        `json:"reason"`
	Data          map[string]any `json:"data"`
	CorrelationID string         `json:"correlation_id"`
	CreatedAt     time.Time      `json:"created_at"`
}

type ShipmentDetail struct {
	Shipment
	Quotes     []Quote     `json:"quotes"`
	Exceptions []Exception `json:"exceptions"`
}

const shipmentSelect = `SELECT s.id, s.reference, s.account_name, s.contact_label, s.origin, s.destination,
	s.cargo_type, to_char(s.required_date, 'YYYY-MM-DD'), s.notes, s.state, s.priority, s.owner_id, u.display_name,
	s.next_action_at, s.next_action_note, s.due_at, s.quoted_at, s.accepted_at, s.scheduled_at, s.schedule_due_at,
	s.pickup_at, s.carrier, s.delivered_at,
	s.received_by, s.delivery_note, s.closed_reason,
	(SELECT count(*) FROM exceptions e WHERE e.organization_id = s.organization_id AND e.shipment_id = s.id AND e.resolved_at IS NULL),
	s.version, s.created_at, s.updated_at
	FROM shipments s LEFT JOIN users u ON u.organization_id = s.organization_id AND u.id = s.owner_id`

func scanShipment(r pgx.Row) (Shipment, error) {
	var sh Shipment
	var ownerID, ownerName *string
	err := r.Scan(&sh.ID, &sh.Reference, &sh.AccountName, &sh.ContactLabel, &sh.Origin, &sh.Destination,
		&sh.CargoType, &sh.RequiredDate, &sh.Notes, &sh.State, &sh.Priority, &ownerID, &ownerName,
		&sh.NextActionAt, &sh.NextActionNote, &sh.DueAt, &sh.QuotedAt, &sh.AcceptedAt, &sh.ScheduledAt, &sh.ScheduleDueAt,
		&sh.PickupAt, &sh.Carrier, &sh.DeliveredAt,
		&sh.ReceivedBy, &sh.DeliveryNote, &sh.ClosedReason, &sh.OpenExceptions,
		&sh.Version, &sh.CreatedAt, &sh.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, ErrNotFound
	}
	if ownerID != nil {
		sh.Owner = &UserRef{ID: *ownerID, DisplayName: deref(ownerName)}
	}
	return sh, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func getShipment(ctx context.Context, q querier, a Actor, id string, now time.Time) (Shipment, error) {
	if !domain.IsUUID(id) {
		return Shipment{}, ErrNotFound
	}
	sh, err := scanShipment(q.QueryRow(ctx, shipmentSelect+` WHERE s.organization_id = $1 AND s.id = $2`, a.Org.ID, id))
	if err != nil {
		return sh, err
	}
	sh.derive(now, a.Org.Loc)
	return sh, nil
}

// EventRecord is what a mutation writes to the audit log.
type EventRecord struct {
	ShipmentID    string
	Action        string
	FromState     *string
	ToState       *string
	Reason        *string
	Data          map[string]any
	CorrelationID string
	At            time.Time
}

func insertEvent(ctx context.Context, q querier, a Actor, e EventRecord) (int64, error) {
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		return 0, err
	}
	var id int64
	err = q.QueryRow(ctx, `INSERT INTO shipment_events
		(organization_id, shipment_id, actor_id, action, from_state, to_state, reason, data, correlation_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		a.Org.ID, e.ShipmentID, a.UserID, e.Action, e.FromState, e.ToState, e.Reason, data, e.CorrelationID, e.At).Scan(&id)
	if err != nil {
		return 0, err
	}
	// Transactional outbox: queue the webhook POST in the same transaction so
	// it can never be lost between commit and delivery.
	var url *string
	var events []string
	if err := q.QueryRow(ctx, `SELECT webhook_url, webhook_events FROM organizations WHERE id = $1`, a.Org.ID).Scan(&url, &events); err != nil {
		return 0, err
	}
	subscribed := len(events) == 0
	for _, action := range events {
		if action == e.Action {
			subscribed = true
			break
		}
	}
	if url != nil && *url != "" && subscribed {
		payload, err := json.Marshal(map[string]any{
			"event":          e.Action,
			"shipment_id":    e.ShipmentID,
			"organization":   a.Org.Slug,
			"actor":          a.Email,
			"from_state":     e.FromState,
			"to_state":       e.ToState,
			"reason":         e.Reason,
			"data":           e.Data,
			"correlation_id": e.CorrelationID,
			"at":             e.At,
		})
		if err != nil {
			return 0, err
		}
		if _, err := q.Exec(ctx, `INSERT INTO notification_deliveries
			(organization_id, event_id, action, url, payload) VALUES ($1, $2, $3, $4, $5)`,
			a.Org.ID, id, e.Action, *url, payload); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateShipment inserts a request in state "new" and its "created" event.
func (s *Store) CreateShipment(ctx context.Context, a Actor, in domain.CreateShipmentInput, now time.Time, corr string) (Shipment, error) {
	var out Shipment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if in.OwnerID != nil {
			ok, err := userInOrg(ctx, tx, a.Org.ID, *in.OwnerID)
			if err != nil {
				return err
			}
			if !ok {
				return unknownOwner("owner_id")
			}
		}
		var prefix string
		var n int
		if err := tx.QueryRow(ctx, `UPDATE organizations SET next_reference = next_reference + 1
			WHERE id = $1 RETURNING reference_prefix, next_reference - 1`, a.Org.ID).Scan(&prefix, &n); err != nil {
			return err
		}
		due, err := dueAtStage(ctx, tx, a, now, in.Priority, sla.StageQuote)
		if err != nil {
			return err
		}
		ref := fmt.Sprintf("%s-%d", prefix, n)
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO shipments (organization_id, reference, account_name, contact_label, origin,
			destination, cargo_type, required_date, notes, priority, owner_id, next_action_at, next_action_note,
			due_at, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $16) RETURNING id`,
			a.Org.ID, ref, in.AccountName, in.ContactLabel, in.Origin, in.Destination, in.CargoType, in.RequiredDate,
			in.Notes, in.Priority, in.OwnerID, in.NextActionAt, in.NextActionNote,
			due, a.UserID, now).Scan(&id)
		if err != nil {
			return err
		}
		if in.OwnerID != nil || in.NextActionAt != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO assignments (organization_id, shipment_id, owner_id, priority,
				next_action_at, next_action_note, assigned_by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				a.Org.ID, id, in.OwnerID, in.Priority, in.NextActionAt, in.NextActionNote, a.UserID, now); err != nil {
				return err
			}
		}
		to := string(workflow.New)
		if _, err := insertEvent(ctx, tx, a, EventRecord{
			ShipmentID: id, Action: "created", ToState: &to, CorrelationID: corr, At: now,
			Data: map[string]any{"reference": ref, "priority": in.Priority, "owner_id": in.OwnerID, "next_action_at": in.NextActionAt},
		}); err != nil {
			return err
		}
		out, err = getShipment(ctx, tx, a, id, now)
		return err
	})
	return out, err
}

// ListFilter narrows GET /api/v1/shipments. Owner is a user ID or "none".
// Offset/Limit implement offset paging over a stable sort.
type ListFilter struct {
	States   []workflow.State
	Owner    string
	Overdue  bool
	DueToday bool
	Query    string
	Limit    int
	Offset   int
}

// overdueSQL mirrors sla.Overdue; $now is the bound parameter for "now".
const overdueSQL = `(s.state NOT IN ('delivered', 'cancelled', 'lost') AND (
	(s.next_action_at IS NOT NULL AND s.next_action_at <= %[1]s) OR
	(s.state IN ('new', 'needs_info') AND s.due_at <= %[1]s) OR
	(s.state = 'accepted' AND s.schedule_due_at IS NOT NULL AND s.schedule_due_at <= %[1]s)))`

// ListShipments returns the requested page plus the total number of matching
// rows, so callers can render "x–y of n" without a second round trip.
func (s *Store) ListShipments(ctx context.Context, a Actor, f ListFilter, now time.Time) ([]Shipment, int, error) {
	where := []string{"s.organization_id = $1"}
	args := []any{a.Org.ID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(f.States) > 0 {
		states := make([]string, len(f.States))
		for i, st := range f.States {
			states[i] = string(st)
		}
		where = append(where, "s.state = ANY("+arg(states)+")")
	}
	switch {
	case f.Owner == "none":
		where = append(where, "s.owner_id IS NULL")
	case f.Owner != "":
		if !domain.IsUUID(f.Owner) {
			return []Shipment{}, 0, nil
		}
		where = append(where, "s.owner_id = "+arg(f.Owner))
	}
	if f.Overdue {
		where = append(where, fmt.Sprintf(overdueSQL, arg(now)))
	}
	if f.DueToday {
		start, end := sla.DayBounds(now, a.Org.Loc)
		where = append(where, "s.next_action_at >= "+arg(start)+" AND s.next_action_at < "+arg(end))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		p := arg(like)
		where = append(where, "(s.reference ILIKE "+p+" OR s.account_name ILIKE "+p+" OR s.origin ILIKE "+p+" OR s.destination ILIKE "+p+")")
	}
	var total int
	if err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM shipments s WHERE "+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	offset := max(f.Offset, 0)
	// ORDER BY ends in a unique tiebreaker (id) so pages are stable under
	// offset paging and concurrent inserts.
	sql := shipmentSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY s.next_action_at ASC NULLS LAST, s.due_at ASC, s.reference ASC, s.id ASC LIMIT " + arg(limit) +
		" OFFSET " + arg(offset)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Shipment, error) { return scanShipment(r) })
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].derive(now, a.Org.Loc)
	}
	return out, total, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) GetShipment(ctx context.Context, a Actor, id string, now time.Time) (ShipmentDetail, error) {
	sh, err := getShipment(ctx, s.pool, a, id, now)
	if err != nil {
		return ShipmentDetail{}, err
	}
	d := ShipmentDetail{Shipment: sh}
	rows, err := s.pool.Query(ctx, `SELECT id, amount_cents, currency, valid_until, status, created_at FROM quotes
		WHERE organization_id = $1 AND shipment_id = $2 ORDER BY created_at DESC`, a.Org.ID, id)
	if err != nil {
		return d, err
	}
	d.Quotes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Quote, error) {
		var q Quote
		err := r.Scan(&q.ID, &q.AmountCents, &q.Currency, &q.ValidUntil, &q.Status, &q.CreatedAt)
		return q, err
	})
	if err != nil {
		return d, err
	}
	rows, err = s.pool.Query(ctx, `SELECT e.id, e.kind, e.reason, e.owner_id, u.display_name, e.follow_up_at, e.opened_at,
		e.resolved_at, e.resolution FROM exceptions e LEFT JOIN users u ON u.organization_id = e.organization_id AND u.id = e.owner_id
		WHERE e.organization_id = $1 AND e.shipment_id = $2 ORDER BY e.opened_at DESC`, a.Org.ID, id)
	if err != nil {
		return d, err
	}
	d.Exceptions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Exception, error) {
		var e Exception
		var oid, oname *string
		err := r.Scan(&e.ID, &e.Kind, &e.Reason, &oid, &oname, &e.FollowUpAt, &e.OpenedAt, &e.ResolvedAt, &e.Resolution)
		if oid != nil {
			e.Owner = &UserRef{ID: *oid, DisplayName: deref(oname)}
		}
		return e, err
	})
	return d, err
}

// Events returns the audit timeline, oldest first.
func (s *Store) Events(ctx context.Context, a Actor, shipmentID string) ([]Event, error) {
	if !domain.IsUUID(shipmentID) {
		return nil, ErrNotFound
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shipments WHERE organization_id = $1 AND id = $2)`,
		a.Org.ID, shipmentID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.action, e.actor_id, u.display_name, e.from_state, e.to_state, e.reason,
		e.data, e.correlation_id, e.created_at FROM shipment_events e
		JOIN users u ON u.organization_id = e.organization_id AND u.id = e.actor_id
		WHERE e.organization_id = $1 AND e.shipment_id = $2 ORDER BY e.id`, a.Org.ID, shipmentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.ID, &e.Action, &e.Actor.ID, &e.Actor.DisplayName, &e.FromState, &e.ToState, &e.Reason,
			&e.Data, &e.CorrelationID, &e.CreatedAt)
		return e, err
	})
}

type lockedShipment struct {
	State          workflow.State
	Version        int
	OwnerID        *string
	Priority       sla.Priority
	NextActionAt   *time.Time
	NextActionNote string
	CreatedAt      time.Time
	AcceptedAt     *time.Time
}

// lockShipment selects the row FOR UPDATE and checks the client's version.
func lockShipment(ctx context.Context, tx pgx.Tx, a Actor, id string, version int) (lockedShipment, error) {
	var l lockedShipment
	if !domain.IsUUID(id) {
		return l, ErrNotFound
	}
	err := tx.QueryRow(ctx, `SELECT state, version, owner_id, priority, next_action_at, next_action_note, created_at
		FROM shipments WHERE organization_id = $1 AND id = $2 FOR UPDATE`, a.Org.ID, id).
		Scan(&l.State, &l.Version, &l.OwnerID, &l.Priority, &l.NextActionAt, &l.NextActionNote, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	if l.Version != version {
		return l, &VersionConflictError{Current: l.Version}
	}
	return l, nil
}

// bump increments the version guarded by the expected version. The row is
// already locked, so a zero-row result means a programming error.
func bump(ctx context.Context, tx pgx.Tx, a Actor, id string, version int, now time.Time, set string, args ...any) error {
	base := []any{a.Org.ID, id, version, now}
	sql := `UPDATE shipments SET version = version + 1, updated_at = $4`
	if set != "" {
		sql += ", " + set
	}
	sql += ` WHERE organization_id = $1 AND id = $2 AND version = $3`
	tag, err := tx.Exec(ctx, sql, append(base, args...)...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return &VersionConflictError{Current: version + 1}
	}
	return nil
}

// Transition applies a workflow command. Errors: ErrNotFound,
// *VersionConflictError, *workflow.InvalidTransitionError,
// *workflow.RequirementsError.
func (s *Store) Transition(ctx context.Context, a Actor, id string, version int, in workflow.TransitionInput, now time.Time, corr string) (Shipment, error) {
	var out Shipment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		l, err := lockShipment(ctx, tx, a, id, version)
		if err != nil {
			return err
		}
		var facts = workflow.Facts{Now: now}
		if err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM quotes WHERE organization_id = $1 AND shipment_id = $2 AND status = 'open'),
			EXISTS (SELECT 1 FROM quotes WHERE organization_id = $1 AND shipment_id = $2 AND status = 'accepted')`,
			a.Org.ID, id).Scan(&facts.HasOpenQuote, &facts.HasAcceptedQuote); err != nil {
			return err
		}
		if err := workflow.Validate(l.State, in, facts); err != nil {
			return err
		}
		data := map[string]any{}
		set := "state = $5"
		args := []any{in.To}
		add := func(col string, v any) {
			args = append(args, v)
			set += fmt.Sprintf(", %s = $%d", col, len(args)+4)
		}
		switch in.To {
		case workflow.Quoted:
			if _, err := tx.Exec(ctx, `UPDATE quotes SET status = 'superseded' WHERE organization_id = $1 AND shipment_id = $2 AND status = 'open'`, a.Org.ID, id); err != nil {
				return err
			}
			var qid string
			if err := tx.QueryRow(ctx, `INSERT INTO quotes (organization_id, shipment_id, amount_cents, currency, valid_until, status, created_by, created_at)
				VALUES ($1, $2, $3, $4, $5, 'open', $6, $7) RETURNING id`, a.Org.ID, id, in.Quote.AmountCents, in.Quote.Currency,
				in.Quote.ValidUntil, a.UserID, now).Scan(&qid); err != nil {
				return err
			}
			set += ", quoted_at = COALESCE(quoted_at, $4)"
			data["quote"] = map[string]any{"id": qid, "amount_cents": in.Quote.AmountCents, "currency": in.Quote.Currency, "valid_until": in.Quote.ValidUntil}
		case workflow.Accepted:
			var qid string
			if err := tx.QueryRow(ctx, `UPDATE quotes SET status = 'accepted' WHERE organization_id = $1 AND shipment_id = $2 AND status = 'open' RETURNING id`,
				a.Org.ID, id).Scan(&qid); err != nil {
				return err
			}
			data["quote_id"] = qid
			set += ", accepted_at = COALESCE(accepted_at, $4)"
			due, err := dueAtStage(ctx, tx, a, now, l.Priority, sla.StageSchedule)
			if err != nil {
				return err
			}
			add("schedule_due_at", due)
			data["schedule_due_at"] = due.Format("2006-01-02T15:04:05Z07:00")
		case workflow.Scheduled:
			add("pickup_at", in.PickupAt)
			add("carrier", strings.TrimSpace(in.Carrier))
			set += ", scheduled_at = COALESCE(scheduled_at, $4)"
			data["pickup_at"], data["carrier"] = in.PickupAt, strings.TrimSpace(in.Carrier)
		case workflow.Delivered:
			add("delivered_at", in.Evidence.DeliveredAt)
			add("received_by", strings.TrimSpace(in.Evidence.ReceivedBy))
			add("delivery_note", strings.TrimSpace(in.Evidence.Note))
			data["evidence"] = map[string]any{"received_by": strings.TrimSpace(in.Evidence.ReceivedBy), "delivered_at": in.Evidence.DeliveredAt, "note": strings.TrimSpace(in.Evidence.Note)}
		case workflow.Cancelled, workflow.Lost:
			add("closed_reason", strings.TrimSpace(in.Reason))
		}
		if in.To.Terminal() {
			set += ", next_action_at = NULL"
		}
		if err := bump(ctx, tx, a, id, version, now, set, args...); err != nil {
			return err
		}
		from, to := string(l.State), string(in.To)
		if _, err := insertEvent(ctx, tx, a, EventRecord{ShipmentID: id, Action: "transitioned", FromState: &from, ToState: &to,
			Reason: strp(strings.TrimSpace(in.Reason)), Data: data, CorrelationID: corr, At: now}); err != nil {
			return err
		}
		out, err = getShipment(ctx, tx, a, id, now)
		return err
	})
	return out, err
}

// Assign changes owner, priority and next action. Changing priority
// recomputes due_at from created_at with the current SLA policy.
func (s *Store) Assign(ctx context.Context, a Actor, id string, in domain.AssignmentInput, now time.Time, corr string) (Shipment, error) {
	var out Shipment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		l, err := lockShipment(ctx, tx, a, id, in.Version)
		if err != nil {
			return err
		}
		if l.State.Terminal() {
			return ErrClosed
		}
		changes := map[string]any{}
		owner, priority, next, note := l.OwnerID, l.Priority, l.NextActionAt, l.NextActionNote
		if in.OwnerID != nil {
			owner = nil
			if *in.OwnerID != "" {
				ok, err := userInOrg(ctx, tx, a.Org.ID, *in.OwnerID)
				if err != nil {
					return err
				}
				if !ok {
					return unknownOwner("owner_id")
				}
				owner = in.OwnerID
			}
			if deref(owner) != deref(l.OwnerID) {
				changes["owner_id"] = map[string]any{"from": l.OwnerID, "to": owner}
			}
		}
		if in.Priority != nil && *in.Priority != l.Priority {
			priority = *in.Priority
			changes["priority"] = map[string]any{"from": l.Priority, "to": priority}
		}
		if in.ClearNext {
			next = nil
		} else if in.NextActionAt != nil {
			t := in.NextActionAt.UTC()
			next = &t
		}
		if !sameTime(next, l.NextActionAt) {
			changes["next_action_at"] = map[string]any{"from": l.NextActionAt, "to": next}
		}
		if in.NextActionNote != nil && strings.TrimSpace(*in.NextActionNote) != l.NextActionNote {
			note = strings.TrimSpace(*in.NextActionNote)
			changes["next_action_note"] = map[string]any{"from": l.NextActionNote, "to": note}
		}
		if len(changes) == 0 {
			out, err = getShipment(ctx, tx, a, id, now)
			return err
		}
		set := "owner_id = $5, priority = $6, next_action_at = $7, next_action_note = $8"
		args := []any{owner, priority, next, note}
		if priority != l.Priority {
			due, err := dueAtStage(ctx, tx, a, l.CreatedAt, priority, sla.StageQuote)
			if err != nil {
				return err
			}
			set += ", due_at = $9"
			args = append(args, due)
			if l.State == workflow.Accepted && l.AcceptedAt != nil {
				sdue, err := dueAtStage(ctx, tx, a, *l.AcceptedAt, priority, sla.StageSchedule)
				if err != nil {
					return err
				}
				set += fmt.Sprintf(", schedule_due_at = $%d", len(args)+5)
				args = append(args, sdue)
			}
		}
		if err := bump(ctx, tx, a, id, in.Version, now, set, args...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assignments (organization_id, shipment_id, owner_id, priority, next_action_at,
			next_action_note, assigned_by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			a.Org.ID, id, owner, priority, next, note, a.UserID, now); err != nil {
			return err
		}
		if _, err := insertEvent(ctx, tx, a, EventRecord{ShipmentID: id, Action: "assigned", Data: map[string]any{"changes": changes},
			CorrelationID: corr, At: now}); err != nil {
			return err
		}
		out, err = getShipment(ctx, tx, a, id, now)
		return err
	})
	return out, err
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// AddNote appends a note to the timeline. Notes never change the shipment
// row, so they do not need a version.
func (s *Store) AddNote(ctx context.Context, a Actor, id, body string, now time.Time, corr string) (Event, error) {
	var ev Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := getShipment(ctx, tx, a, id, now); err != nil {
			return err
		}
		eid, err := insertEvent(ctx, tx, a, EventRecord{ShipmentID: id, Action: "note_added",
			Data: map[string]any{"body": strings.TrimSpace(body)}, CorrelationID: corr, At: now})
		if err != nil {
			return err
		}
		ev = Event{ID: eid, Action: "note_added", Actor: UserRef{ID: a.UserID, DisplayName: a.DisplayName},
			Data: map[string]any{"body": strings.TrimSpace(body)}, CorrelationID: corr, CreatedAt: now}
		return nil
	})
	return ev, err
}

// OpenException records an exception and makes its follow-up the
// shipment's next action (and its owner the shipment owner, if given).
func (s *Store) OpenException(ctx context.Context, a Actor, id string, in domain.EventInput, now time.Time, corr string) (Shipment, error) {
	var out Shipment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		l, err := lockShipment(ctx, tx, a, id, in.Version)
		if err != nil {
			return err
		}
		if l.State.Terminal() {
			return ErrClosed
		}
		owner := l.OwnerID
		if in.OwnerID != nil && *in.OwnerID != "" {
			ok, err := userInOrg(ctx, tx, a.Org.ID, *in.OwnerID)
			if err != nil {
				return err
			}
			if !ok {
				return unknownOwner("owner_id")
			}
			owner = in.OwnerID
		}
		reason := strings.TrimSpace(in.Reason)
		var exID string
		if err := tx.QueryRow(ctx, `INSERT INTO exceptions (organization_id, shipment_id, kind, reason, owner_id, follow_up_at, opened_by, opened_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, a.Org.ID, id, in.ExceptionKind, reason, owner,
			in.FollowUpAt.UTC(), a.UserID, now).Scan(&exID); err != nil {
			return err
		}
		note := truncate("Exception follow-up ("+in.ExceptionKind+"): "+reason, 200)
		if err := bump(ctx, tx, a, id, in.Version, now, "owner_id = $5, next_action_at = $6, next_action_note = $7",
			owner, in.FollowUpAt.UTC(), note); err != nil {
			return err
		}
		if _, err := insertEvent(ctx, tx, a, EventRecord{ShipmentID: id, Action: "exception_opened", Reason: &reason,
			Data:          map[string]any{"exception_id": exID, "kind": in.ExceptionKind, "follow_up_at": in.FollowUpAt.UTC(), "owner_id": owner},
			CorrelationID: corr, At: now}); err != nil {
			return err
		}
		out, err = getShipment(ctx, tx, a, id, now)
		return err
	})
	return out, err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ResolveException closes an open exception.
func (s *Store) ResolveException(ctx context.Context, a Actor, id string, in domain.EventInput, now time.Time, corr string) (Event, error) {
	var ev Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		res := strings.TrimSpace(in.Resolution)
		tag, err := tx.Exec(ctx, `UPDATE exceptions SET resolved_by = $4, resolved_at = $5, resolution = $6
			WHERE organization_id = $1 AND shipment_id = $2 AND id = $3 AND resolved_at IS NULL`,
			a.Org.ID, id, in.ExceptionID, a.UserID, now, res)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		data := map[string]any{"exception_id": in.ExceptionID}
		eid, err := insertEvent(ctx, tx, a, EventRecord{ShipmentID: id, Action: "exception_resolved", Reason: &res,
			Data: data, CorrelationID: corr, At: now})
		ev = Event{ID: eid, Action: "exception_resolved", Actor: UserRef{ID: a.UserID, DisplayName: a.DisplayName},
			Reason: &res, Data: data, CorrelationID: corr, CreatedAt: now}
		return err
	})
	return ev, err
}
