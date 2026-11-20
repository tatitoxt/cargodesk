"use client";

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, CARGO_TYPES, newKey, PRIORITIES, type Shipment } from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { ErrorBox } from "@/components/FieldErrors";

function NewShipment() {
  const me = useMe();
  const router = useRouter();
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // One key per form instance: double submits create a single request.
  const key = useRef(newKey());

  if (me.role === "viewer") {
    return (<><Nav /><p className="error">Viewers cannot create requests.</p></>);
  }

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const body = Object.fromEntries(["account_name", "contact_label", "origin", "destination", "cargo_type", "required_date", "priority", "notes"]
      .map((k) => [k, String(f.get(k) ?? "")]));
    setBusy(true);
    try {
      const sh = await api<Shipment>("/shipments", { method: "POST", body, idempotencyKey: key.current });
      router.push(`/shipments/${sh.id}`);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  }

  return (
    <>
      <Nav />
      <h1>New request</h1>
      <form className="card form" onSubmit={submit} aria-label="New request">
        <label>Account<input name="account_name" required maxLength={120} /></label>
        <label>Contact<input name="contact_label" required maxLength={120} /></label>
        <label>Origin<input name="origin" required maxLength={200} /></label>
        <label>Destination<input name="destination" required maxLength={200} /></label>
        <label>
          Cargo type
          <select name="cargo_type" defaultValue="general">
            {CARGO_TYPES.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
        </label>
        <label>Required date<input name="required_date" type="date" required /></label>
        <label>
          Priority
          <select name="priority" defaultValue="normal">
            {PRIORITIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </label>
        <label className="full">Notes<textarea name="notes" maxLength={2000} /></label>
        <div className="full"><ErrorBox error={error} /></div>
        <div className="full"><button type="submit" disabled={busy}>Create request</button></div>
      </form>
    </>
  );
}

export default function Page() {
  return (
    <RequireSession>
      <NewShipment />
    </RequireSession>
  );
}
