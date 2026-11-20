// Thin client for the Go API, reached through the same-origin /api/v1 proxy.

export type Role = "viewer" | "coordinator" | "admin";
export type State =
  | "new" | "needs_info" | "quoted" | "accepted" | "scheduled"
  | "in_transit" | "delivered" | "cancelled" | "lost";
export type Priority = "low" | "normal" | "high" | "urgent";

export const STATES: State[] = ["new", "needs_info", "quoted", "accepted", "scheduled", "in_transit", "delivered", "cancelled", "lost"];
export const PRIORITIES: Priority[] = ["low", "normal", "high", "urgent"];
export const CARGO_TYPES = ["general", "palletized", "refrigerated", "hazardous", "bulk", "oversized"];
export const EXCEPTION_KINDS = ["delay", "damage", "missing_info", "other"];

export const STATE_LABELS: Record<State, string> = {
  new: "New", needs_info: "Needs info", quoted: "Quoted", accepted: "Accepted", scheduled: "Scheduled",
  in_transit: "In transit", delivered: "Delivered", cancelled: "Cancelled", lost: "Lost",
};

export interface UserRef { id: string; display_name: string }
export interface Me {
  id: string; email: string; display_name: string; role: Role;
  organization: { id: string; slug: string; name: string; timezone: string };
}
export interface DemoUser { id: string; email: string; display_name: string; role: Role; organization_name: string }
export interface User { id: string; email: string; display_name: string; role: Role }

export interface Shipment {
  id: string; reference: string; account_name: string; contact_label: string; origin: string; destination: string;
  cargo_type: string; required_date: string; notes: string; state: State; priority: Priority; owner: UserRef | null;
  next_action_at: string | null; next_action_note: string; due_at: string; quoted_at: string | null;
  pickup_at: string | null; carrier: string | null; delivered_at: string | null; received_by: string | null;
  delivery_note: string | null; closed_reason: string | null; open_exceptions: number; version: number;
  created_at: string; updated_at: string; sla_status: string; overdue: boolean; due_today: boolean;
  allowed_transitions: State[];
}
export interface Quote { id: string; amount_cents: number; currency: string; valid_until: string | null; status: string; created_at: string }
export interface ShipmentException {
  id: string; kind: string; reason: string; owner: UserRef | null; follow_up_at: string; opened_at: string;
  resolved_at: string | null; resolution: string | null;
}
export interface ShipmentDetail extends Shipment { quotes: Quote[]; exceptions: ShipmentException[] }
export interface TimelineEvent {
  id: number; action: string; actor: UserRef; from_state: string | null; to_state: string | null;
  reason: string | null; data: Record<string, unknown> | null; correlation_id: string; created_at: string;
}

export interface FieldError { field: string; message: string }
export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public body: Record<string, unknown>) {
    super(message);
  }
  get fields(): FieldError[] {
    return (this.body.fields as FieldError[] | undefined) ?? [];
  }
}

export async function api<T>(path: string, init: { method?: string; body?: unknown; idempotencyKey?: string } = {}): Promise<T> {
  const headers: Record<string, string> = {};
  if (init.body !== undefined) headers["Content-Type"] = "application/json";
  if (init.idempotencyKey) headers["Idempotency-Key"] = init.idempotencyKey;
  const res = await fetch(`/api/v1${path}`, {
    method: init.method ?? "GET",
    headers,
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    credentials: "same-origin",
    cache: "no-store",
  });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? "error", data.message ?? res.statusText, data);
  }
  return data as T;
}

export function newKey(): string {
  return crypto.randomUUID();
}

export function formatDateTime(iso: string | null | undefined, tz: string): string {
  if (!iso) return "—";
  return new Intl.DateTimeFormat("en-GB", {
    timeZone: tz, day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", hour12: false,
  }).format(new Date(iso));
}

export function formatMoney(cents: number, currency: string): string {
  return new Intl.NumberFormat("en-US", { style: "currency", currency }).format(cents / 100);
}

// datetime-local values are in the browser's timezone; convert to ISO UTC.
export function localToISO(v: string): string | undefined {
  return v ? new Date(v).toISOString() : undefined;
}
