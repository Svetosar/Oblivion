import { useState, type FormEvent } from "react";
import { ApiError, api, errMsg, type Session } from "./api";

function Form({ title, children, onSubmit, submit, error, busy }: {
  title: string;
  children: React.ReactNode;
  onSubmit: () => void;
  submit: string;
  error: string;
  busy: boolean;
}) {
  return (
    <main className="login">
      <a className="brand" href="#/" aria-label="Админка">
        <i className="dot" />
        Админка
      </a>
      <form
        className="card center-card"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        <h1>{title}</h1>
        {error && (
          <div className="err-box" role="alert">
            {error}
          </div>
        )}
        {children}
        <button className="primary" disabled={busy} style={{ width: "100%", minHeight: 48, marginTop: 20 }}>
          {submit}
        </button>
      </form>
    </main>
  );
}

// After this many failed attempts in a row the sign-in screen offers a gentle way out.
const GENTLE_HINT_AFTER = 3;

export function Login({ onDone, note }: { onDone: (s: Session) => void; note?: string }) {
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  const [err, setErr] = useState("");
  const [fails, setFails] = useState(0);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      onDone(await api<Session>("POST", "/auth/login", { username: u, password: p }));
    } catch (e) {
      // A wrong login or password gets one plain message that does not say which one is wrong.
      if (e instanceof ApiError && e.status === 401) {
        setErr("Имя пользователя или пароль не подошли. Проверьте раскладку клавиатуры и Caps Lock.");
        setFails((n) => n + 1);
        setP("");
      } else {
        setErr(errMsg(e));
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <Form title="Вход в редакцию" onSubmit={() => void submit()} submit="Войти" error={err} busy={busy}>
      {note && (
        <div className="warn" role="alert">
          {note}
        </div>
      )}
      <label>
        Имя пользователя
        <input value={u} onChange={(e) => setU(e.target.value)} autoComplete="username" autoFocus required style={{ minHeight: 48 }} />
      </label>
      <label>
        Пароль
        <input type="password" value={p} onChange={(e) => setP(e.target.value)} autoComplete="current-password" required style={{ minHeight: 48 }} />
      </label>
      {fails >= GENTLE_HINT_AFTER && (
        <p className="muted" style={{ margin: "16px 0 0", fontSize: "var(--text-sm)" }}>
          Не получается войти? Ничего страшного: администратор редакции может задать вам новый пароль.
        </p>
      )}
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
    <Form title="Создайте администратора" onSubmit={() => void submit()} submit="Создать аккаунт" error={err} busy={busy}>
      <p className="muted">
        Пользователей пока нет. Одноразовый код установки записан в журнале сервера при запуске (ищите <code>setup_token</code>).
      </p>
      <label>
        Код установки
        <input value={token} onChange={(e) => setToken(e.target.value.trim())} autoComplete="off" required />
      </label>
      <label>
        Имя пользователя
        <input value={u} onChange={(e) => setU(e.target.value)} autoComplete="username" required minLength={3} maxLength={32} />
      </label>
      <label>
        Пароль
        <input type="password" value={p} onChange={(e) => setP(e.target.value)} autoComplete="new-password" required minLength={15} />
        <span className="help">Не меньше 15 символов: латинские буквы, цифры и знаки</span>
      </label>
    </Form>
  );
}
