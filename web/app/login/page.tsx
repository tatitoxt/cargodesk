"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { api, type DemoUser } from "@/lib/api";
import { ErrorBox } from "@/components/FieldErrors";

function Login() {
  const [users, setUsers] = useState<DemoUser[]>([]);
  const [error, setError] = useState<unknown>(null);
  const router = useRouter();
  const next = useSearchParams().get("next");

  useEffect(() => {
    api<{ users: DemoUser[] }>("/auth/demo-users").then((r) => setUsers(r.users)).catch(setError);
  }, []);

  async function signIn(id: string) {
    try {
      await api("/auth/demo-login", { method: "POST", body: { user_id: id } });
      router.replace(next && next.startsWith("/") && !next.startsWith("//") ? next : "/");
    } catch (e) {
      setError(e);
    }
  }

  return (
    <>
      <h1>Sign in to CargoDesk (demo)</h1>
      <p className="muted">
        Demo sign-in: pick a seeded synthetic user. There are no passwords; this mode is for local demos only.
      </p>
      <ErrorBox error={error} />
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
