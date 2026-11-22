"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";
import { ErrorBox } from "@/components/FieldErrors";

const TIMEZONES = [
  "UTC",
  "America/Argentina/Buenos_Aires",
  "America/New_York",
  "America/Chicago",
  "America/Los_Angeles",
  "America/Sao_Paulo",
  "Europe/Madrid",
  "Europe/London",
  "Europe/Berlin",
];

export default function SignupPage() {
  const router = useRouter();
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    const f = new FormData(e.currentTarget);
    try {
      await api("/signup", {
        method: "POST",
        body: {
          slug: f.get("slug"),
          name: f.get("name"),
          timezone: f.get("timezone"),
          reference_prefix: f.get("reference_prefix"),
          email: f.get("email"),
          display_name: f.get("display_name"),
          password: f.get("password"),
        },
      });
      router.replace("/");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Create your organization</h1>
      <p className="muted">
        Provisions a fresh CargoDesk organization and signs you in as its admin — no seed needed.
      </p>
      <ErrorBox error={error} />
      <form className="card row" onSubmit={onSubmit} aria-label="Organization sign-up">
        <label>
          Organization slug
          <input required name="slug" placeholder="acme-freight" pattern="[a-z0-9\-]{2,40}" />
        </label>
        <label>
          Organization name
          <input required name="name" placeholder="Acme Freight" maxLength={120} />
        </label>
        <label>
          Reference prefix
          <input required name="reference_prefix" placeholder="AF" pattern="[A-Za-z]{2,4}" maxLength={4} />
        </label>
        <label>
          Timezone
          <select name="timezone" defaultValue="UTC">
            {TIMEZONES.map((tz) => (
              <option key={tz} value={tz}>{tz}</option>
            ))}
          </select>
        </label>
        <label>
          Your name
          <input required name="display_name" placeholder="Ana Owner" maxLength={120} />
        </label>
        <label>
          Work email
          <input required name="email" type="email" placeholder="you@acme-freight.example" />
        </label>
        <label>
          Password
          <input required name="password" type="password" minLength={10} placeholder="10+ characters" />
        </label>
        <button type="submit" disabled={busy}>{busy ? "Creating…" : "Create organization"}</button>
      </form>
      <p className="muted">
        Already have an account? <Link href="/login">Sign in</Link>
      </p>
    </>
  );
}
