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

  // act runs an API call, shows the outcome, and reports whether it succeeded.
  const act = async (fn: () => Promise<unknown>, ok: string): Promise<boolean> => {
    setMsg(null);
    try {
      await fn();
      setMsg({ ok: true, text: ok });
      load();
      return true;
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
      return false;
    }
  };

  const [resetFor, setResetFor] = useState<number | null>(null);
  const [resetPw, setResetPw] = useState("");
  const submitReset = (e: FormEvent, u: User) => {
    e.preventDefault();
    void act(() => api("PATCH", `/auth/users/${u.id}`, { password: resetPw }), "Password reset. That user's sessions were ended.").then((done) => {
      if (done) {
        setResetFor(null);
        setResetPw("");
      }
    });
  };

  return (
    <>
      <h1>Users</h1>
      {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
      <CreateUser onCreated={() => { setMsg({ ok: true, text: "User created." }); load(); }} onError={(t) => setMsg({ ok: false, text: t })} />
      {users && (
        <table className="stack">
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
                <td>
                  <span className="mobile-only muted">Created </span>
                  {fmtDate(u.created_at)}
                </td>
                <td className="wide">
                  {/* A div: display:flex on the cell itself breaks the row borders. */}
                  <div className="actions">
                    <button onClick={() => void act(() => api("PATCH", `/auth/users/${u.id}`, { disabled: !u.disabled }), u.disabled ? "User enabled." : "User disabled.")}>{u.disabled ? "Enable" : "Disable"}</button>
                    {resetFor === u.id ? (
                      <form className="inline-reset" onSubmit={(e) => submitReset(e, u)}>
                        <input type="password" value={resetPw} onChange={(e) => setResetPw(e.target.value)} placeholder="New password (12+ characters)" aria-label={`New password for ${u.username}`} minLength={12} required autoComplete="new-password" autoFocus />
                        <button className="primary">Set</button>
                        <button type="button" onClick={() => { setResetFor(null); setResetPw(""); }}>Cancel</button>
                      </form>
                    ) : (
                      <button onClick={() => { setResetFor(u.id); setResetPw(""); }}>Reset password</button>
                    )}
                    <button className="danger" onClick={() => confirm(`Delete ${u.username}?`) && void act(() => api("DELETE", `/auth/users/${u.id}`), "User deleted.")}>
                      Delete
                    </button>
                  </div>
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
