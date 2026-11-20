"use client";

import { useEffect, useState } from "react";
import { api, PRIORITIES, type Priority } from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { ErrorBox } from "@/components/FieldErrors";

type Hours = Record<Priority, number>;

function Settings() {
  const me = useMe();
  const [hours, setHours] = useState<Hours | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const canEdit = me.role === "admin";

  useEffect(() => {
    api<{ hours: Hours }>("/settings/sla").then((r) => setHours(r.hours)).catch(setError);
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaved(false);
    setError(null);
    try {
      const r = await api<{ hours: Hours }>("/settings/sla", { method: "PUT", body: { hours } });
      setHours(r.hours);
      setSaved(true);
    } catch (err) {
      setError(err);
    }
  }

  return (
    <>
      <Nav />
      <h1>Internal quote SLA</h1>
      <p className="muted">
        Hours from request creation until a quote must be sent. Internal target only, not a customer commitment.
        Timezone: {me.organization.timezone}.
      </p>
      {hours && (
        <form className="card row" onSubmit={save} aria-label="SLA policy">
          {PRIORITIES.map((p) => (
            <label key={p}>
              {p} (hours)
              <input type="number" min={1} max={720} value={hours[p]} disabled={!canEdit}
                onChange={(e) => setHours({ ...hours, [p]: Number(e.target.value) })} />
            </label>
          ))}
          {canEdit ? <button type="submit">Save policy</button> : <span className="muted">Only admins can change the policy.</span>}
          {saved && <span className="success" role="status">Saved</span>}
        </form>
      )}
      <ErrorBox error={error} />
    </>
  );
}

export default function Page() {
  return (
    <RequireSession>
      <Settings />
    </RequireSession>
  );
}
