"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";
import { useMe } from "@/lib/session";

export function Nav() {
  const me = useMe();
  const router = useRouter();
  async function logout() {
    await api("/auth/logout", { method: "POST" });
    router.replace("/login");
  }
  return (
    <nav className="nav" aria-label="Main">
      <strong>CargoDesk</strong>
      <Link href="/">Requests</Link>
      {me.role !== "viewer" && <Link href="/shipments/new">New request</Link>}
      <Link href="/settings">SLA policy</Link>
      <span className="spacer" />
      <span data-testid="current-user">
        {me.display_name} · <span className="badge">{me.role}</span> · {me.organization.name}
      </span>
      <button className="secondary" onClick={logout}>Sign out</button>
    </nav>
  );
}
