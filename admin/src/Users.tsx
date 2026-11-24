import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errMsg, fmtDate, type Me, type User } from "./api";

const ROLES = ["admin", "editor", "author"];

export function UsersPage({ me }: { me: Me }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const load = useCallback(() => {
    api<User[]>("GET", "/auth/users").then(setUsers, (e) => setMsg({ ok: false, text: errMsg(e) }));
  }, []);
  useEffect(load, [load]);

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    setMsg(null);
    try {
      await fn();
      setMsg({ ok: true, text: ok });
      load();
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  const resetPassword = (u: User) => {
    const pw = prompt(`New password for ${u.username} (at least 12 characters). Their sessions will be ended.`);
    if (pw) void act(() => api("PATCH", `/auth/users/${u.id}`, { password: pw }), "Password reset.");
  };

  return (
    <>
      <h1>Users</h1>
      {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
      <CreateUser onCreated={() => { setMsg({ ok: true, text: "User created." }); load(); }} onError={(t) => setMsg({ ok: false, text: t })} />
      {users && (
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Role</th>
              <th>Status</th>
              <th>Created</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>
                  {u.username} {u.id === me.id && <span className="muted">(you)</span>}
                </td>
                <td>
                  <select value={u.role} onChange={(e) => void act(() => api("PATCH", `/auth/users/${u.id}`, { role: e.target.value }), "Role changed. Sessions of that user were ended.")} aria-label={`Role of ${u.username}`}>
                    {ROLES.map((r) => (
                      <option key={r}>{r}</option>
                    ))}
                  </select>
                </td>
                <td>
                  <span className={"tag " + (u.disabled ? "draft" : "published")}>{u.disabled ? "disabled" : "active"}</span>
                </td>
                <td>{fmtDate(u.created_at)}</td>
                <td className="actions">
                  <button onClick={() => void act(() => api("PATCH", `/auth/users/${u.id}`, { disabled: !u.disabled }), u.disabled ? "User enabled." : "User disabled.")}>{u.disabled ? "Enable" : "Disable"}</button>
                  <button onClick={() => resetPassword(u)}>Reset password</button>
                  <button className="danger" onClick={() => confirm(`Delete ${u.username}?`) && void act(() => api("DELETE", `/auth/users/${u.id}`), "User deleted.")}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

function CreateUser({ onCreated, onError }: { onCreated: () => void; onError: (t: string) => void }) {
  const [username, setU] = useState("");
  const [password, setP] = useState("");
  const [role, setRole] = useState("author");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api("POST", "/auth/users", { username, password, role });
      setU("");
      setP("");
      onCreated();
    } catch (e) {
      onError(errMsg(e));
    }
  };
  return (
    <form className="card inline-form" onSubmit={(e) => void submit(e)}>
      <label>
        Username
        <input value={username} onChange={(e) => setU(e.target.value)} required minLength={3} maxLength={32} autoComplete="off" />
      </label>
      <label>
        Password
        <input type="password" value={password} onChange={(e) => setP(e.target.value)} required minLength={12} autoComplete="new-password" />
      </label>
      <label>
        Role
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          {ROLES.map((r) => (
            <option key={r}>{r}</option>
          ))}
        </select>
      </label>
      <button className="primary">Add user</button>
    </form>
  );
}
