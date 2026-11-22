"use client";

import Link from "next/link";
import { use, useCallback, useEffect, useState } from "react";
import {
  api, ApiError, EXCEPTION_KINDS, formatDateTime, formatMoney, localToISO, newKey, PRIORITIES, STATE_LABELS,
  type Priority, type ShipmentDetail, type State, type TimelineEvent, type User,
} from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { SLABadge, StateBadge } from "@/components/Badges";
import { ErrorBox } from "@/components/FieldErrors";

const NEEDS_REASON: State[] = ["needs_info", "cancelled", "lost"];

function Detail({ id }: { id: string }) {
  const me = useMe();
  const tz = me.organization.timezone;
  const canEdit = me.role !== "viewer";
  const [sh, setSh] = useState<ShipmentDetail | null>(null);
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [notFound, setNotFound] = useState(false);
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    try {
      const [d, ev] = await Promise.all([
        api<ShipmentDetail>(`/shipments/${id}`),
        api<{ events: TimelineEvent[] }>(`/shipments/${id}/events`),
      ]);
      setSh(d);
      setEvents(ev.events);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(e);
    }
  }, [id]);

  useEffect(() => {
    load();
    api<{ users: User[] }>("/users").then((r) => setUsers(r.users)).catch(setError);
  }, [load]);

  // Every mutation reloads the shipment and timeline from the server.
  async function mutate(fn: () => Promise<unknown>): Promise<boolean> {
    setError(null);
    try {
      await fn();
      await load();
      return true;
    } catch (e) {
      setError(e);
      return false;
    }
  }

  if (notFound) return (<><Nav /><p className="error">Request not found.</p><Link href="/">Back to requests</Link></>);
  if (!sh) return (<><Nav /><ErrorBox error={error} /><p className="muted">Loading…</p></>);

  const userName = (uid: unknown) => users.find((u) => u.id === uid)?.display_name ?? (uid ? String(uid) : "nobody");
  const conflict = error instanceof ApiError && error.code === "version_conflict";

  return (
    <>
      <Nav />
      <p><Link href="/">← Requests</Link></p>
      <h1>
        <span data-testid="reference">{sh.reference}</span> · {sh.account_name}{" "}
        <StateBadge state={sh.state} /> <SLABadge status={sh.sla_status} />
        {sh.overdue && <> <span className="badge overdue">Overdue</span></>}
      </h1>
      <p style={{ display: "flex", gap: 8 }}>
        <button
          type="button"
          className="secondary"
          data-testid="copy-track-link"
          onClick={async () => {
            await navigator.clipboard.writeText(`${location.origin}/track/${sh.track_token}`);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          }}
        >
          {copied ? "Copied!" : "Copy tracking link"}
        </button>
        {canEdit && (
          <button
            type="button"
            className="secondary"
            onClick={async () => {
              if (!confirm("Rotate the public tracking link? The previous link will stop working.")) return;
              const r = await api<{ track_token: string }>(`/shipments/${id}/track-link`, { method: "POST" });
              setSh({ ...sh, track_token: r.track_token });
            }}
          >
            Rotate link
          </button>
        )}
      </p>
      {error !== null && (
        <div>
          <ErrorBox error={error} />
          {conflict && <button className="secondary" onClick={() => { setError(null); load(); }}>Reload latest version</button>}
        </div>
      )}
      <div className="grid">
        <div>
          <section className="card">
            <h2>Request</h2>
            <dl>
              <dt>Contact</dt><dd>{sh.contact_label}</dd>
              <dt>Route</dt><dd>{sh.origin} → {sh.destination}</dd>
              <dt>Cargo</dt><dd>{sh.cargo_type}</dd>
              <dt>Required date</dt><dd>{sh.required_date}</dd>
              <dt>Priority</dt><dd data-testid="priority">{sh.priority}</dd>
              <dt>Owner</dt><dd data-testid="owner">{sh.owner?.display_name ?? "Unassigned"}</dd>
              <dt>Next action</dt><dd>{formatDateTime(sh.next_action_at, tz)} {sh.next_action_note}</dd>
              <dt>Quote due</dt><dd>{formatDateTime(sh.due_at, tz)}</dd>
              {sh.pickup_at && (<><dt>Pickup</dt><dd>{formatDateTime(sh.pickup_at, tz)} · {sh.carrier}</dd></>)}
              {sh.delivered_at && (<><dt>Delivered</dt><dd data-testid="evidence">{formatDateTime(sh.delivered_at, tz)} · received by {sh.received_by}{sh.delivery_note ? ` · ${sh.delivery_note}` : ""}</dd></>)}
              {sh.closed_reason && (<><dt>Closed reason</dt><dd>{sh.closed_reason}</dd></>)}
              <dt>Notes</dt><dd className="body">{sh.notes || "—"}</dd>
              <dt>Version</dt><dd>{sh.version}</dd>
            </dl>
            {sh.quotes.length > 0 && (
              <>
                <h2 style={{ marginTop: 12 }}>Quotes</h2>
                <ul>
                  {sh.quotes.map((q) => (
                    <li key={q.id}>{formatMoney(q.amount_cents, q.currency)} · {q.status}{q.valid_until ? ` · valid until ${formatDateTime(q.valid_until, tz)}` : ""}</li>
                  ))}
                </ul>
              </>
            )}
          </section>
          {canEdit && sh.allowed_transitions.length > 0 && (
            <TransitionPanel sh={sh} onSubmit={(body) => mutate(() => api(`/shipments/${id}/transitions`,
              { method: "POST", body: { ...body, version: sh.version }, idempotencyKey: newKey() }))} />
          )}
          {!canEdit && <p className="muted" data-testid="read-only">Read-only: your role ({me.role}) cannot change requests.</p>}
          <section className="card">
            <h2>Timeline</h2>
            <ol className="timeline" aria-label="Timeline">
              {events.map((ev) => (
                <li key={ev.id} data-testid="event">
                  <div className="meta">{formatDateTime(ev.created_at, tz)} · {ev.actor.display_name} · <code>{ev.correlation_id}</code></div>
                  <div><strong>{describe(ev, userName)}</strong></div>
                  {ev.reason && <div className="body">Reason: {ev.reason}</div>}
                  {ev.action === "note_added" && <div className="body">{String(ev.data?.body ?? "")}</div>}
                </li>
              ))}
            </ol>
          </section>
        </div>
        <div>
          {canEdit && sh.allowed_transitions.length > 0 && (
            <AssignmentPanel key={sh.version} sh={sh} users={users}
              onSubmit={(body) => mutate(() => api(`/shipments/${id}/assignment`, { method: "PATCH", body: { ...body, version: sh.version } }))} />
          )}
          {canEdit && (
            <NotePanel onSubmit={(text) => mutate(() => api(`/shipments/${id}/events`, { method: "POST", body: { kind: "note", body: text } }))} />
          )}
          <ExceptionsPanel sh={sh} users={users} canOpen={canEdit && sh.allowed_transitions.length > 0} canResolve={canEdit} tz={tz}
            onOpen={(body) => mutate(() => api(`/shipments/${id}/events`, { method: "POST", body: { ...body, kind: "exception", version: sh.version } }))}
            onResolve={(exId, resolution) => mutate(() => api(`/shipments/${id}/events`,
              { method: "POST", body: { kind: "exception_resolved", exception_id: exId, resolution } }))} />
        </div>
      </div>
    </>
  );
}

function describe(ev: TimelineEvent, userName: (id: unknown) => string): string {
  const label = (s: string | null) => (s ? STATE_LABELS[s as State] ?? s : "");
  switch (ev.action) {
    case "created": return "Request created";
    case "transitioned": {
      let s = `${label(ev.from_state)} → ${label(ev.to_state)}`;
      const quote = ev.data?.quote as { amount_cents: number; currency: string } | undefined;
      if (quote) s += ` (${formatMoney(quote.amount_cents, quote.currency)})`;
      if (ev.data?.carrier) s += ` (carrier ${ev.data.carrier})`;
      const evidence = ev.data?.evidence as { received_by: string } | undefined;
      if (evidence) s += ` (received by ${evidence.received_by})`;
      return s;
    }
    case "assigned": {
      const changes = (ev.data?.changes ?? {}) as Record<string, { from: unknown; to: unknown }>;
      return "Updated " + Object.entries(changes).map(([k, v]) =>
        k === "owner_id" ? `owner: ${userName(v.from)} → ${userName(v.to)}` : `${k.replaceAll("_", " ")}: ${v.from ?? "—"} → ${v.to ?? "—"}`,
      ).join("; ");
    }
    case "note_added": return "Note";
    case "exception_opened": return `Exception opened (${ev.data?.kind})`;
    case "exception_resolved": return "Exception resolved";
    default: return ev.action;
  }
}

type Body = Record<string, unknown>;

function TransitionPanel({ sh, onSubmit }: { sh: ShipmentDetail; onSubmit: (b: Body) => Promise<boolean> }) {
  const [to, setTo] = useState<State | null>(null);
  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const get = (k: string) => String(f.get(k) ?? "").trim();
    const body: Body = { to };
    if (get("reason")) body.reason = get("reason");
    if (to === "quoted") {
      body.quote = { amount_cents: Math.round(Number(get("amount")) * 100), currency: get("currency").toUpperCase(), valid_until: localToISO(get("valid_until")) };
    }
    if (to === "scheduled") Object.assign(body, { pickup_at: localToISO(get("pickup_at")), carrier: get("carrier") });
    if (to === "delivered") {
      body.evidence = { received_by: get("received_by"), delivered_at: localToISO(get("delivered_at")), note: get("evidence_note") };
    }
    if (await onSubmit(body)) setTo(null);
  }
  const reasonRequired = to !== null && (NEEDS_REASON.includes(to) || (to === "new" && sh.state === "needs_info"));
  return (
    <section className="card" aria-label="Move request">
      <h2>Move request</h2>
      <div className="row">
        {sh.allowed_transitions.map((s) => (
          <button key={s} type="button" className={to === s ? "" : "secondary"} onClick={() => setTo(s)}>
            {s === "new" ? "Back to New" : STATE_LABELS[s]}
          </button>
        ))}
      </div>
      {to && (
        <form className="form" style={{ marginTop: 10 }} onSubmit={submit} aria-label={`Move to ${STATE_LABELS[to]}`}>
          {to === "quoted" && (<>
            <label>Amount<input name="amount" type="number" step="0.01" min="0.01" required /></label>
            <label>Currency<input name="currency" defaultValue="USD" maxLength={3} required /></label>
            <label>Valid until (optional)<input name="valid_until" type="datetime-local" /></label>
          </>)}
          {to === "scheduled" && (<>
            <label>Pickup at<input name="pickup_at" type="datetime-local" required /></label>
            <label>Carrier<input name="carrier" required maxLength={120} /></label>
          </>)}
          {to === "delivered" && (<>
            <label>Received by<input name="received_by" required maxLength={120} /></label>
            <label>Delivered at<input name="delivered_at" type="datetime-local" required /></label>
            <label className="full">Evidence note<input name="evidence_note" maxLength={500} placeholder="e.g. signed delivery note number" /></label>
          </>)}
          {(reasonRequired || to === "accepted" || to === "in_transit") && (
            <label className="full">Reason{reasonRequired ? "" : " (optional)"}<textarea name="reason" maxLength={500} required={reasonRequired} /></label>
          )}
          <div className="full"><button type="submit" className={to === "cancelled" || to === "lost" ? "danger" : ""}>Confirm: {STATE_LABELS[to]}</button></div>
        </form>
      )}
    </section>
  );
}

function toLocalInput(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

function AssignmentPanel({ sh, users, onSubmit }: { sh: ShipmentDetail; users: User[]; onSubmit: (b: Body) => Promise<boolean> }) {
  const [owner, setOwner] = useState(sh.owner?.id ?? "");
  const [priority, setPriority] = useState<Priority>(sh.priority);
  const [next, setNext] = useState(toLocalInput(sh.next_action_at));
  const [note, setNote] = useState(sh.next_action_note);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const body: Body = {};
    if (owner !== (sh.owner?.id ?? "")) body.owner_id = owner;
    if (priority !== sh.priority) body.priority = priority;
    if (next !== toLocalInput(sh.next_action_at)) {
      if (next) body.next_action_at = localToISO(next);
      else body.clear_next_action = true;
    }
    if (note !== sh.next_action_note) body.next_action_note = note;
    await onSubmit(body);
  }
  return (
    <form className="card" onSubmit={submit} aria-label="Assignment">
      <h2>Owner &amp; next action</h2>
      <div className="form">
        <label className="full">
          Owner
          <select value={owner} onChange={(e) => setOwner(e.target.value)}>
            <option value="">Unassigned</option>
            {users.filter((u) => u.role !== "viewer").map((u) => <option key={u.id} value={u.id}>{u.display_name}</option>)}
          </select>
        </label>
        <label>
          Priority
          <select value={priority} onChange={(e) => setPriority(e.target.value as Priority)}>
            {PRIORITIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </label>
        <label>Next action<input type="datetime-local" value={next} onChange={(e) => setNext(e.target.value)} /></label>
        <label className="full">Next action note<input value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} /></label>
        <div className="full"><button type="submit">Save assignment</button></div>
      </div>
    </form>
  );
}

function NotePanel({ onSubmit }: { onSubmit: (text: string) => Promise<boolean> }) {
  const [text, setText] = useState("");
  return (
    <form className="card" aria-label="Add note" onSubmit={async (e) => { e.preventDefault(); if (await onSubmit(text)) setText(""); }}>
      <h2>Add note</h2>
      <textarea value={text} onChange={(e) => setText(e.target.value)} maxLength={2000} required aria-label="Note" style={{ width: "100%" }} />
      <button type="submit">Add note</button>
    </form>
  );
}

function ExceptionsPanel({ sh, users, canOpen, canResolve, tz, onOpen, onResolve }: {
  sh: ShipmentDetail; users: User[]; canOpen: boolean; canResolve: boolean; tz: string;
  onOpen: (b: Body) => Promise<boolean>; onResolve: (id: string, resolution: string) => Promise<boolean>;
}) {
  async function open(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    const owner = String(f.get("owner_id") ?? "");
    const ok = await onOpen({ exception_kind: f.get("exception_kind"), reason: String(f.get("reason") ?? "").trim(),
      follow_up_at: localToISO(String(f.get("follow_up_at") ?? "")) ?? null, owner_id: owner || undefined });
    if (ok) form.reset();
  }
  return (
    <section className="card" aria-label="Exceptions">
      <h2>Exceptions ({sh.open_exceptions} open)</h2>
      <ul>
        {sh.exceptions.map((x) => (
          <li key={x.id} data-testid="exception">
            <strong>{x.kind}</strong>: {x.reason} · follow up {formatDateTime(x.follow_up_at, tz)}
            {x.resolved_at ? <span className="success"> · resolved: {x.resolution}</span> : canResolve && (
              <form className="row" onSubmit={async (e) => {
                e.preventDefault();
                await onResolve(x.id, String(new FormData(e.currentTarget).get("resolution") ?? "").trim());
              }}>
                <input name="resolution" placeholder="Resolution" required aria-label="Resolution" />
                <button type="submit" className="secondary">Resolve</button>
              </form>
            )}
          </li>
        ))}
      </ul>
      {canOpen && (
        <form className="form" onSubmit={open} aria-label="Open exception">
          <label>
            Kind
            <select name="exception_kind" defaultValue="delay">{EXCEPTION_KINDS.map((k) => <option key={k}>{k}</option>)}</select>
          </label>
          <label>Follow up at<input name="follow_up_at" type="datetime-local" required /></label>
          <label className="full">Reason<input name="reason" required maxLength={500} /></label>
          <label className="full">
            Owner
            <select name="owner_id" defaultValue="">
              <option value="">Keep current owner</option>
              {users.filter((u) => u.role !== "viewer").map((u) => <option key={u.id} value={u.id}>{u.display_name}</option>)}
            </select>
          </label>
          <div className="full"><button type="submit" className="secondary">Open exception</button></div>
        </form>
      )}
    </section>
  );
}

export default function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <RequireSession>
      <Detail id={id} />
    </RequireSession>
  );
}
