"use client";

import { use, useEffect, useState } from "react";
import { STATE_LABELS, formatDateTime, type State } from "@/lib/api";
import { StateBadge } from "@/components/Badges";

type PublicEvent = {
  action: string;
  from_state: string | null;
  to_state: string | null;
  created_at: string;
};

type PublicShipment = {
  organization: string;
  reference: string;
  origin: string;
  destination: string;
  cargo_type: string;
  required_date: string;
  state: State;
  carrier: string | null;
  delivered_at: string | null;
  received_by: string | null;
  events: PublicEvent[];
};

const ACTION_LABELS: Record<string, string> = {
  created: "Shipment created",
  transition: "Status updated",
  assigned: "Assigned",
  note_added: "Note",
  exception_opened: "Issue reported",
  exception_resolved: "Issue resolved",
};

function eventLabel(e: PublicEvent): string {
  const base = ACTION_LABELS[e.action] ?? e.action;
  if (e.to_state && STATE_LABELS[e.to_state as State]) {
    return `${base} — ${STATE_LABELS[e.to_state as State]}`;
  }
  return base;
}

export default function TrackPage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = use(params);
  const [sh, setSh] = useState<PublicShipment | null>(null);
  const [missing, setMissing] = useState(false);

  useEffect(() => {
    fetch(`/api/v1/track/${encodeURIComponent(token)}`, { cache: "no-store" })
      .then(async (res) => {
        if (res.ok) setSh((await res.json()) as PublicShipment);
        else setMissing(true);
      })
      .catch(() => setMissing(true));
  }, [token]);

  if (missing) {
    return (
      <div className="card" style={{ maxWidth: 560, margin: "40px auto" }}>
        <h1>Tracking link not found</h1>
        <p className="meta">This link is invalid or has expired. Ask the sender for a new tracking link.</p>
      </div>
    );
  }
  if (!sh) {
    return (
      <div className="card" style={{ maxWidth: 560, margin: "40px auto" }}>
        <p className="meta">Loading…</p>
      </div>
    );
  }

  return (
    <div style={{ maxWidth: 640, margin: "24px auto", padding: "0 12px" }}>
      <p className="meta">{sh.organization}</p>
      <div className="card">
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8 }}>
          <h1 style={{ margin: 0 }}>{sh.reference}</h1>
          <StateBadge state={sh.state} />
        </div>
        <p style={{ margin: "8px 0 0" }}>
          <strong>{sh.origin}</strong> → <strong>{sh.destination}</strong>
        </p>
        <p className="meta" style={{ margin: "4px 0 0" }}>
          {sh.cargo_type} · Required by {sh.required_date}
          {sh.carrier ? ` · Carrier: ${sh.carrier}` : ""}
        </p>
        {sh.delivered_at && (
          <p className="meta" style={{ margin: "4px 0 0" }}>
            Delivered {formatDateTime(sh.delivered_at, "UTC")}
            {sh.received_by ? ` — received by ${sh.received_by}` : ""}
          </p>
        )}
      </div>

      <div className="card">
        <h2 style={{ marginTop: 0 }}>Tracking history</h2>
        <ul className="timeline">
          {sh.events.map((e, i) => (
            <li key={i}>
              <div className="meta">{formatDateTime(e.created_at, "UTC")}</div>
              <div>{eventLabel(e)}</div>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
