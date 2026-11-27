import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ROLE_RU, api, errMsg, fmtDate, type Me, type User } from "./api";

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

  // A role change is applied only after "Сохранить роль": it ends that user's sessions
  // and may hand out administrator rights, so one stray click must not do it.
  const [roleDraft, setRoleDraft] = useState<Record<number, string>>({});
  const [resetFor, setResetFor] = useState<number | null>(null);
  const [resetPw, setResetPw] = useState("");
  const submitReset = (e: FormEvent, u: User) => {
    e.preventDefault();
    void act(() => api("PATCH", `/auth/users/${u.id}`, { password: resetPw }), "Пароль задан заново. Сессии этого пользователя завершены.").then((done) => {
      if (done) {
        setResetFor(null);
        setResetPw("");
      }
    });
  };

  return (
    <>
      <div className="top">
        <h1>Пользователи</h1>
      </div>
      {msg && (
        <div className={msg.ok ? "ok-box" : "err-box"} role="alert">
          {msg.text}
        </div>
      )}
      {users && (
        <table className="stack">
          <thead>
            <tr>
              <th>Пользователь</th>
              <th>Роль</th>
              <th>Статус</th>
              <th>Создан</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => {
              const draft = roleDraft[u.id] ?? u.role;
              const changed = draft !== u.role;
              return (
                <tr key={u.id}>
                  <td>
                    {u.username} {u.id === me.id && <span className="tag">это вы</span>}
                  </td>
                  <td>
                    <select value={draft} onChange={(e) => setRoleDraft({ ...roleDraft, [u.id]: e.target.value })} aria-label={`Роль пользователя ${u.username}`}>
                      {ROLES.map((r) => (
                        <option key={r} value={r}>
                          {ROLE_RU[r]}
                        </option>
                      ))}
                    </select>
                    {changed && <div className="help" style={{ color: "var(--color-warning-text)" }}>Роль ещё не сохранена</div>}
                  </td>
                  <td>
                    <span className={"tag " + (u.disabled ? "disabled" : "active")}>{u.disabled ? "Отключён" : "Активен"}</span>
                  </td>
                  <td>
                    <span className="mobile-only muted">Создан </span>
                    {fmtDate(u.created_at)}
                  </td>
                  <td className="wide">
                    {/* A div: display:flex on the cell itself breaks the row borders. */}
                    <div className="actions">
                      {changed ? (
                        <>
                          <button
                            className="primary sm"
                            onClick={() =>
                              void act(() => api("PATCH", `/auth/users/${u.id}`, { role: draft }), "Роль изменена. Сессии этого пользователя завершены.").then((done) => {
                                if (done) setRoleDraft(({ [u.id]: _drop, ...rest }) => rest);
                              })
                            }
                          >
                            Сохранить роль
                          </button>
                          <button className="ghost sm" onClick={() => setRoleDraft(({ [u.id]: _drop, ...rest }) => rest)}>
                            Отмена
                          </button>
                        </>
                      ) : (
                        <>
                          <button className="sm" onClick={() => void act(() => api("PATCH", `/auth/users/${u.id}`, { disabled: !u.disabled }), u.disabled ? "Пользователь включён." : "Пользователь отключён.")}>
                            {u.disabled ? "Включить" : "Отключить"}
                          </button>
                          {resetFor === u.id ? (
                            <form className="inline-reset" onSubmit={(e) => submitReset(e, u)}>
                              <input type="password" value={resetPw} onChange={(e) => setResetPw(e.target.value)} placeholder="Новый пароль, от 15 символов" aria-label={`Новый пароль для ${u.username}`} minLength={15} required autoComplete="new-password" autoFocus />
                              <button className="primary sm">Задать</button>
                              <button type="button" className="ghost sm" onClick={() => { setResetFor(null); setResetPw(""); }}>
                                Отмена
                              </button>
                            </form>
                          ) : (
                            <button className="sm" onClick={() => { setResetFor(u.id); setResetPw(""); }}>
                              Сбросить пароль
                            </button>
                          )}
                          <button className="danger sm" onClick={() => confirm(`Удалить пользователя ${u.username}? Это нельзя отменить.`) && void act(() => api("DELETE", `/auth/users/${u.id}`), "Пользователь удалён.")}>
                            Удалить
                          </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      <CreateUser onCreated={() => { setMsg({ ok: true, text: "Пользователь добавлен." }); load(); }} onError={(t) => setMsg({ ok: false, text: t })} />
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
    <form className="card" onSubmit={(e) => void submit(e)}>
      <h2>Новый пользователь</h2>
      <div className="inline-form">
        <label>
          Имя пользователя
          <input value={username} onChange={(e) => setU(e.target.value)} required minLength={3} maxLength={32} autoComplete="off" />
          <span className="help">От 3 до 32 символов</span>
        </label>
        <label>
          Пароль
          <input type="password" value={password} onChange={(e) => setP(e.target.value)} required minLength={15} autoComplete="new-password" />
          <span className="help">Не меньше 15 символов, латиница</span>
        </label>
        <label style={{ flex: "0 1 12rem" }}>
          Роль
          <select value={role} onChange={(e) => setRole(e.target.value)} style={{ width: "100%" }}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {ROLE_RU[r]}
              </option>
            ))}
          </select>
          <span className="help">Автор пишет, редактор публикует</span>
        </label>
        <button className="primary" style={{ marginBottom: 22 }}>
          Добавить пользователя
        </button>
      </div>
    </form>
  );
}
