"use client";

import { useEffect, useState } from "react";
import { api, type Role, type User } from "@/lib/api";
import { RequireSession, useMe } from "@/lib/session";
import { Nav } from "@/components/Nav";
import { ErrorBox } from "@/components/FieldErrors";

const ROLES: Role[] = ["viewer", "coordinator", "admin"];

function Members() {
  const me = useMe();
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [tempPassword, setTempPassword] = useState<{ email: string; password: string } | null>(null);
  const isAdmin = me.role === "admin";

  function refresh() {
    api<{ users: User[] }>("/users").then((r) => setUsers(r.users)).catch(setError);
  }
  useEffect(refresh, []);

  async function invite(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const r = await api<{ user: User; temporary_password: string }>("/users/invite", {
        method: "POST", body: { email, display_name: name, role },
      });
      setTempPassword({ email, password: r.temporary_password });
      setEmail("");
      setName("");
      setRole("viewer");
      refresh();
    } catch (err) {
      setError(err);
    }
  }

  async function changeRole(u: User, next: Role) {
    setError(null);
    try {
      await api(`/users/${u.id}`, { method: "PATCH", body: { role: next } });
      refresh();
    } catch (err) {
      setError(err);
    }
  }

  async function remove(u: User) {
    if (!confirm(`Remove ${u.display_name} (${u.email})? Their sessions end immediately.`)) return;
    setError(null);
    try {
      await api(`/users/${u.id}`, { method: "DELETE" });
      refresh();
    } catch (err) {
      setError(err);
    }
  }

  return (
    <>
      <Nav />
      <h1>Members</h1>
      <p className="muted">People in {me.organization.name}. Removing a member ends their sessions.</p>
      {tempPassword && (
        <div className="card" role="status" aria-label="Temporary password">
          <p>
            <strong>{tempPassword.email}</strong> was invited. Share this temporary password — it is
            shown once and must be replaced at first sign-in:
          </p>
          <p><code style={{ fontSize: "1.1em" }}>{tempPassword.password}</code></p>
          <button className="secondary" onClick={() => setTempPassword(null)}>Done — I copied it</button>
        </div>
      )}
      {isAdmin ? (
        <form className="card row" onSubmit={invite} aria-label="Invite member">
          <label>
            Email
            <input required type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
          <label>
            Name
            <input required value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label>
            Role
            <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
              {ROLES.map((r) => (
                <option key={r} value={r}>{r}</option>
              ))}
            </select>
          </label>
          <button type="submit">Invite member</button>
        </form>
      ) : (
        <p className="muted">Only admins can manage members.</p>
      )}
      {users && (
        <table aria-label="Member roster">
          <thead>
            <tr><th>Name</th><th>Email</th><th>Role</th>{isAdmin && <th />}</tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>{u.display_name}{u.id === me.id && <span className="muted"> (you)</span>}</td>
                <td>{u.email}</td>
                <td>
                  {isAdmin && u.id !== me.id ? (
                    <select
                      value={u.role}
                      aria-label={`Role for ${u.email}`}
                      onChange={(e) => changeRole(u, e.target.value as Role)}
                    >
                      {ROLES.map((r) => (
                        <option key={r} value={r}>{r}</option>
                      ))}
                    </select>
                  ) : (
                    <span className="badge">{u.role}</span>
                  )}
                </td>
                {isAdmin && (
                  <td>
                    {u.id !== me.id && (
                      <button className="secondary" onClick={() => remove(u)}>Remove</button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <ErrorBox error={error} />
    </>
  );
}

export default function Page() {
  return (
    <RequireSession>
      <Members />
    </RequireSession>
  );
}
