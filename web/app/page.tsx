"use client";

import Link from "next/link";
import { Suspense, useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, formatDateTime, STATE_LABELS, STATES, type SavedView, type Shipment, type User } from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { SLABadge, StateBadge } from "@/components/Badges";
import { ErrorBox } from "@/components/FieldErrors";

const FILTER_KEYS = ["state", "owner", "overdue", "due", "q"] as const;
const PAGE_SIZE = 50;

function Board() {
  const me = useMe();
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const [items, setItems] = useState<Shipment[] | null>(null);
  const [total, setTotal] = useState(0);
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [q, setQ] = useState(params.get("q") ?? "");
  const [views, setViews] = useState<SavedView[]>([]);
  const [activeView, setActiveView] = useState("");
  const [saving, setSaving] = useState(false);
  const [viewName, setViewName] = useState("");

  // Filters live in the URL so refresh and shared links keep them.
  const query = new URLSearchParams();
  for (const k of FILTER_KEYS) {
    const v = params.get(k);
    if (v) query.set(k, v);
  }
  const qs = query.toString();
  // Page is in the URL too; the API call translates it to limit/offset.
  const page = Math.max(1, parseInt(params.get("page") ?? "1", 10) || 1);
  const apiQuery = new URLSearchParams(query);
  apiQuery.set("limit", String(PAGE_SIZE));
  apiQuery.set("offset", String((page - 1) * PAGE_SIZE));
  const apiQs = apiQuery.toString();

  useEffect(() => {
    setItems(null);
    api<{ items: Shipment[]; total: number }>(`/shipments?${apiQs}`)
      .then((r) => { setItems(r.items); setTotal(r.total); })
      .catch(setError);
  }, [apiQs]);
  useEffect(() => {
    api<{ users: User[] }>("/users").then((r) => setUsers(r.users)).catch(setError);
  }, []);
  useEffect(() => {
    api<{ views: SavedView[] }>("/views").then((r) => setViews(r.views)).catch(setError);
  }, []);

  function setFilter(key: string, value: string) {
    const next = new URLSearchParams(qs);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("page"); // any filter change goes back to page 1
    setActiveView(""); // filters no longer match a saved view
    router.replace(`${pathname}${next.size ? `?${next}` : ""}`);
  }

  function applyView(v: SavedView) {
    const next = new URLSearchParams();
    for (const [k, val] of Object.entries(v.filters)) next.set(k, val);
    setActiveView(v.id);
    setQ(v.filters["q"] ?? "");
    router.replace(`${pathname}${next.size ? `?${next}` : ""}`);
  }

  async function saveView() {
    const filters: Record<string, string> = {};
    for (const k of FILTER_KEYS) {
      const v = params.get(k);
      if (v) filters[k] = v;
    }
    const v = await api<SavedView>("/views", { method: "POST", body: { name: viewName, filters } });
    setViews((vs) => [...vs, v].sort((a, b) => a.name.localeCompare(b.name)));
    setActiveView(v.id);
    setViewName("");
    setSaving(false);
  }

  async function deleteView(id: string) {
    await api(`/views/${id}`, { method: "DELETE" });
    setViews((vs) => vs.filter((v) => v.id !== id));
    if (activeView === id) setActiveView("");
  }

  function goToPage(p: number) {
    const next = new URLSearchParams(qs);
    if (p > 1) next.set("page", String(p));
    else next.delete("page");
    router.replace(`${pathname}${next.size ? `?${next}` : ""}`);
  }

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const from = items?.length ? (page - 1) * PAGE_SIZE + 1 : 0;
  const to = (page - 1) * PAGE_SIZE + (items?.length ?? 0);

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
        {views.length > 0 && (
          <label>
            Saved view
            <select
              value={activeView}
              onChange={(e) => {
                const v = views.find((x) => x.id === e.target.value);
                setActiveView(e.target.value);
                if (v) applyView(v);
              }}
            >
              <option value="">—</option>
              {views.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}
            </select>
          </label>
        )}
        <button type="button" className="secondary" disabled={!qs} onClick={() => setSaving((s) => !s)}>
          Save view
        </button>
        {activeView && (
          <button type="button" className="secondary" onClick={() => deleteView(activeView).catch(setError)}>
            Delete view
          </button>
        )}
        <span style={{ flex: 1 }} />
        {me.role !== "viewer" && (
          <a href={`/api/v1/exports/shipments.csv${qs ? `?${qs}` : ""}`} download>Export CSV</a>
        )}
      </form>
      {saving && (
        <form
          className="card filters"
          onSubmit={(e) => { e.preventDefault(); saveView().catch(setError); }}
          aria-label="Save view"
        >
          <label>
            View name
            <input value={viewName} onChange={(e) => setViewName(e.target.value)} maxLength={60} required />
          </label>
          <button type="submit" className="secondary">Save current filters</button>
        </form>
      )}
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
      {items !== null && total > 0 && (
        <nav className="pagination muted" aria-label="Pages">
          <span>Showing {from}–{to} of {total}</span>
          {pages > 1 && (
            <>
              <button type="button" className="secondary" disabled={page <= 1} onClick={() => goToPage(page - 1)}>
                Previous
              </button>
              <span>Page {page} of {pages}</span>
              <button type="button" className="secondary" disabled={page >= pages} onClick={() => goToPage(page + 1)}>
                Next
              </button>
            </>
          )}
        </nav>
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
