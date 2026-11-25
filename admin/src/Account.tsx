import { useState, type FormEvent } from "react";
import { api, errMsg, type Me } from "./api";

export function Account({ me }: { me: Me }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    try {
      await api("POST", "/auth/password", { current: cur, new: next });
      setCur("");
      setNext("");
      setMsg({ ok: true, text: "Password changed. Your other sessions were signed out." });
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };
  return (
    <>
      <h1>Account</h1>
      <p className="muted">
        {me.username} · {me.role}
      </p>
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h2>Change password</h2>
        <label>
          Current password
          <input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" required />
        </label>
        <label>
          New password (Latin letters, digits and symbols, at least 15 characters)
          <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" required minLength={15} />
        </label>
        {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
        <button className="primary">Change password</button>
      </form>
    </>
  );
}
