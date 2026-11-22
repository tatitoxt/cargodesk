"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { safeNext } from "@/lib/safe-next";
import { api, type DemoUser, type Me } from "@/lib/api";
import { ErrorBox } from "@/components/FieldErrors";

// Only same-origin paths are allowed as post-login redirects.

function Login() {
  const [users, setUsers] = useState<DemoUser[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [pwEmail, setPwEmail] = useState("");
  const [pwPass, setPwPass] = useState("");
  const router = useRouter();
  const next = useSearchParams().get("next");

  useEffect(() => {
    api<{ users: DemoUser[] }>("/auth/demo-users").then((r) => setUsers(r.users)).catch(setError);
  }, []);

  async function signIn(id: string) {
    try {
      await api("/auth/demo-login", { method: "POST", body: { user_id: id } });
      router.replace(safeNext(next));
    } catch (e) {
      setError(e);
    }
  }

  async function passwordSignIn(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const res = await api<Me & { must_change_password?: boolean }>("/auth/password-login", {
        method: "POST", body: { email: pwEmail, password: pwPass },
      });
      router.replace(res.must_change_password ? `/password?next=${encodeURIComponent(safeNext(next))}` : safeNext(next));
    } catch (err) {
      setError(err);
    }
  }

  return (
    <>
      <h1>Sign in to CargoDesk (demo)</h1>
      <p className="muted">
        Demo sign-in: pick a seeded synthetic user. There are no passwords; this mode is for local demos only.
      </p>
      <ErrorBox error={error} />
      <form className="card row" onSubmit={passwordSignIn} aria-label="Password sign-in">
        <label>
          Email
          <input required type="email" value={pwEmail} onChange={(e) => setPwEmail(e.target.value)} />
        </label>
        <label>
          Password
          <input required type="password" value={pwPass} onChange={(e) => setPwPass(e.target.value)} />
        </label>
        <button type="submit">Sign in</button>
      </form>
      <p className="muted">
        New here? <a href="/signup">Create an organization</a> — or sign in as a seeded demo user:
      </p>
      <div className="users">
        {users.map((u) => (
          <button key={u.id} onClick={() => signIn(u.id)} aria-label={`Sign in as ${u.display_name}`}>
            <strong>{u.display_name}</strong>
            <br />
            <span className="badge">{u.role}</span> <span className="muted">{u.organization_name}</span>
          </button>
        ))}
      </div>
    </>
  );
}

export default function LoginPage() {
  return (
    <Suspense>
      <Login />
    </Suspense>
  );
}
