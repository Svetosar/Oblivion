import { useState, type FormEvent } from "react";
import { api, errMsg, type Session } from "./api";

function Form({ title, children, onSubmit, submit, error, busy }: {
  title: string;
  children: React.ReactNode;
  onSubmit: () => void;
  submit: string;
  error: string;
  busy: boolean;
}) {
  return (
    <form
      className="card center-card"
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <h1>{title}</h1>
      {children}
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy}>
        {submit}
      </button>
    </form>
  );
}

export function Login({ onDone, note }: { onDone: (s: Session) => void; note?: string }) {
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      onDone(await api<Session>("POST", "/auth/login", { username: u, password: p }));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Form title="Sign in" onSubmit={() => void submit()} submit="Sign in" error={err} busy={busy}>
      {note && <p className="warn" role="alert">{note}</p>}
      <label>
        Username
        <input value={u} onChange={(e) => setU(e.target.value)} autoComplete="username" autoFocus required />
      </label>
      <label>
        Password
        <input type="password" value={p} onChange={(e) => setP(e.target.value)} autoComplete="current-password" required />
      </label>
    </Form>
  );
}

export function Setup({ onDone }: { onDone: (s: Session) => void }) {
  const [token, setToken] = useState("");
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      onDone(await api<Session>("POST", "/auth/setup", { token, username: u, password: p }));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Form title="Create the administrator" onSubmit={() => void submit()} submit="Create account" error={err} busy={busy}>
      <p className="muted">
        No users exist yet. The setup token was written to the server log when it started (look for <code>setup_token</code>).
      </p>
      <label>
        Setup token
        <input value={token} onChange={(e) => setToken(e.target.value.trim())} autoComplete="off" required />
      </label>
      <label>
        Username
        <input value={u} onChange={(e) => setU(e.target.value)} autoComplete="username" required minLength={3} maxLength={32} />
      </label>
      <label>
        Password (Latin letters, digits and symbols, at least 15 characters)
        <input type="password" value={p} onChange={(e) => setP(e.target.value)} autoComplete="new-password" required minLength={15} />
      </label>
    </Form>
  );
}
