"use client";

import Link from "next/link";
import { Suspense, useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, formatDateTime, STATE_LABELS, STATES, type Shipment, type User } from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { SLABadge, StateBadge } from "@/components/Badges";
import { ErrorBox } from "@/components/FieldErrors";

const FILTER_KEYS = ["state", "owner", "overdue", "due", "q"] as const;

function Board() {
  const me = useMe();
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const [items, setItems] = useState<Shipment[] | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [q, setQ] = useState(params.get("q") ?? "");

  // Filters live in the URL so refresh and shared links keep them.
  const query = new URLSearchParams();
  for (const k of FILTER_KEYS) {
    const v = params.get(k);
    if (v) query.set(k, v);
  }
  const qs = query.toString();

  useEffect(() => {
    setItems(null);
    api<{ items: Shipment[] }>(`/shipments${qs ? `?${qs}` : ""}`)
      .then((r) => setItems(r.items))
      .catch(setError);
  }, [qs]);
  useEffect(() => {
    api<{ users: User[] }>("/users").then((r) => setUsers(r.users)).catch(setError);
  }, []);

  function setFilter(key: string, value: string) {
    const next = new URLSearchParams(qs);
    if (value) next.set(key, value);
    else next.delete(key);
    router.replace(`${pathname}${next.size ? `?${next}` : ""}`);
  }

  const tz = me.organization.timezone;
  return (
    <>
      <Nav />
      <h1>Requests</h1>
      <form className="card filters" onSubmit={(e) => { e.preventDefault(); setFilter("q", q.trim()); }} aria-label="Filters">
        <label>
          State
          <select value={params.get("state") ?? ""} onChange={(e) => setFilter("state", e.target.value)}>
            <option value="">All states</option>
            {STATES.map((s) => <option key={s} value={s}>{STATE_LABELS[s]}</option>)}
          </select>
        </label>
        <label>
          Owner
          <select value={params.get("owner") ?? ""} onChange={(e) => setFilter("owner", e.target.value)}>
            <option value="">Anyone</option>
            <option value="me">Me</option>
            <option value="none">Unassigned</option>
            {users.map((u) => <option key={u.id} value={u.id}>{u.display_name}</option>)}
          </select>
        </label>
        <label className="inline">
          <input type="checkbox" checked={params.get("overdue") === "true"}
            onChange={(e) => setFilter("overdue", e.target.checked ? "true" : "")} />
          Overdue only
        </label>
        <label className="inline">
          <input type="checkbox" checked={params.get("due") === "today"}
            onChange={(e) => setFilter("due", e.target.checked ? "today" : "")} />
          Next action today
        </label>
        <label>
          Search
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Reference, account, place" />
        </label>
        <button type="submit" className="secondary">Search</button>
        <span style={{ flex: 1 }} />
        {me.role !== "viewer" && (
          <a href={`/api/v1/exports/shipments.csv${qs ? `?${qs}` : ""}`} download>Export CSV</a>
        )}
      </form>
      <ErrorBox error={error} />
      {items === null ? (
        <p className="muted">Loading…</p>
      ) : items.length === 0 ? (
        <p className="muted" data-testid="empty">No requests match these filters.</p>
      ) : (
        <table aria-label="Requests">
          <thead>
            <tr>
              <th>Reference</th><th>Account</th><th>Route</th><th>State</th><th>Priority</th>
              <th>Owner</th><th>Next action</th><th>Quote SLA</th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id} className={s.overdue ? "overdue" : ""} data-testid={`row-${s.reference}`}>
                <td><Link href={`/shipments/${s.id}`}>{s.reference}</Link></td>
                <td>{s.account_name}</td>
                <td>{s.origin} → {s.destination}</td>
                <td><StateBadge state={s.state} /></td>
                <td>{s.priority}</td>
                <td>{s.owner?.display_name ?? <span className="muted">Unassigned</span>}</td>
                <td>
                  {formatDateTime(s.next_action_at, tz)}
                  {s.overdue && <> <span className="badge overdue">Overdue</span></>}
                  {s.next_action_note && <div className="muted">{s.next_action_note}</div>}
                </td>
                <td><SLABadge status={s.sla_status} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

export default function Home() {
  return (
    <RequireSession>
      <Suspense>
        <Board />
      </Suspense>
    </RequireSession>
  );
}
