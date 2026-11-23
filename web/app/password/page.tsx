"use client";

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { safeNext } from "@/lib/safe-next";
import { api } from "@/lib/api";
import { RequireSession } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { ErrorBox } from "@/components/FieldErrors";


function ChangePassword() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<unknown>(null);
  const router = useRouter();
  const dest = safeNext(useSearchParams().get("next"));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (next !== confirm) {
      setError(new Error("new passwords do not match"));
      return;
    }
    try {
      await api("/me/password", { method: "POST", body: { current_password: current, new_password: next } });
      router.replace(dest);
    } catch (err) {
      setError(err);
    }
  }

  return (
    <>
      <Nav />
      <h1>Set a new password</h1>
      <p className="muted">
        You signed in with a temporary password. Choose a new one (at least 10 characters) to continue.
      </p>
      <form className="card row" onSubmit={submit} aria-label="Change password">
        <label>
          Current password
          <input required type="password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        </label>
        <label>
          New password
          <input required type="password" minLength={10} value={next} onChange={(e) => setNext(e.target.value)} />
        </label>
        <label>
          Repeat new password
          <input required type="password" minLength={10} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        </label>
        <button type="submit">Change password</button>
      </form>
      <ErrorBox error={error} />
    </>
  );
}

export default function PasswordPage() {
  return (
    <RequireSession>
      <Suspense>
        <ChangePassword />
      </Suspense>
    </RequireSession>
  );
}
