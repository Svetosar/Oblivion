import { useState, type FormEvent } from "react";
import { ROLE_RU, api, errMsg, type Me } from "./api";

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
      setMsg({ ok: true, text: "Пароль изменён. Другие ваши сессии завершены." });
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };
  return (
    <>
      <div className="top">
        <h1>Аккаунт</h1>
      </div>
      <p className="muted" style={{ margin: 0 }}>
        {me.username}, {ROLE_RU[me.role] ?? me.role}
      </p>
      <form className="card" onSubmit={(e) => void submit(e)} style={{ maxWidth: "34rem" }}>
        <h2>Смена пароля</h2>
        <label>
          Текущий пароль
          <input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" required />
        </label>
        <label>
          Новый пароль
          <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" required minLength={15} />
          <span className="help">Не меньше 15 символов: латинские буквы, цифры и знаки</span>
        </label>
        {msg && (
          <div className={msg.ok ? "ok-box" : "err-box"} role="alert" style={{ marginTop: 16 }}>
            {msg.text}
          </div>
        )}
        <div style={{ marginTop: 20 }}>
          <button className="primary">Сменить пароль</button>
        </div>
      </form>
    </>
  );
}
