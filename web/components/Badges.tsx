import { STATE_LABELS, type State } from "@/lib/api";

const SLA_LABELS: Record<string, string> = {
  on_track: "On track", at_risk: "At risk", breached: "Breached", met: "Met", missed: "Missed", not_applicable: "n/a",
};

export function StateBadge({ state }: { state: State }) {
  return <span className={`badge ${state}`} data-testid="state">{STATE_LABELS[state]}</span>;
}

export function SLABadge({ status }: { status: string }) {
  return <span className={`badge ${status}`} data-testid="sla">{SLA_LABELS[status] ?? status}</span>;
}
