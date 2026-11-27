import { useCallback, useEffect, useState } from "react";
import { ApiError, ROLE_RU, api, can, errMsg, onUnauthorized, setCsrf, type Session } from "./api";
import { dropUnsaved, go, hasUnsaved, mayLeave, useHash } from "./hooks";
import { Login, Setup } from "./Auth";
import { ContentEditor, ContentList } from "./Content";
import { MediaPage } from "./Media";
import { UsersPage } from "./Users";
import { Account } from "./Account";
import { SettingsPage } from "./Settings";
import { Icon, type IconName } from "./icons";

type State = "loading" | "login" | "setup" | { error: string } | Session;

export function App() {
  const [state, setState] = useState<State>("loading");
  // The session ended while a page held unsaved work: ask to sign in again, but
  // keep that page (hidden) so the text survives.
  const [expired, setExpired] = useState(false);

  const probe = useCallback(async () => {
    try {
      const s = await api<Session>("GET", "/auth/me");
      setCsrf(s.csrf);
      setState(s);
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 401) return setState({ error: errMsg(e) });
      try {
        const r = await api<{ available: boolean }>("GET", "/auth/setup");
        setState(r.available ? "setup" : "login");
      } catch {
        setState("login");
      }
    }
  }, []);

  useEffect(() => {
    // With nothing to lose, drop the whole admin: no old data stays in the page.
    onUnauthorized(() => (hasUnsaved() ? setExpired(true) : setState("login")));
    void probe();
  }, [probe]);

  const entered = (s: Session) => {
    setCsrf(s.csrf);
    // Refresh to get the permission map, which login/setup responses do not carry.
    void probe();
    go("/");
  };

  const reentered = async (previousUser: number, s: Session) => {
    setCsrf(s.csrf);
    let full: Session;
    try {
      full = await api<Session>("GET", "/auth/me");
    } catch {
      return; // stay on the sign-in form; submitting it again retries
    }
    if (full.user.id !== previousUser) {
      dropUnsaved(); // someone else must not see or inherit that text
      go("/");
    }
    setCsrf(full.csrf);
    setState(full);
    setExpired(false);
  };

  const loggedOut = () => {
    setExpired(false);
    setState("login");
  };

  if (state === "loading") return <p className="center muted">Загрузка…</p>;
  if (typeof state === "object" && "error" in state)
    return (
      <div className="center">
        <p className="error">Не удалось связаться с сервером: {state.error}</p>
        <button onClick={() => void probe()}>Повторить</button>
      </div>
    );
  if (state === "setup") return <Setup onDone={entered} />;
  if (state === "login") return <Login onDone={entered} />;
  const userId = state.user.id;
  return (
    <>
      {expired && <Login note="Сессия закончилась. Войдите снова: несохранённый текст сохранён." onDone={(s) => void reentered(userId, s)} />}
      <div hidden={expired}>
        <Shell key={userId} session={state} onLogout={loggedOut} />
      </div>
    </>
  );
}

function Shell({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const hash = useHash();
  const me = session.user;
  // The editor of an existing item learns its kind only after loading; it reports it
  // so the right menu entry can be highlighted.
  const [editorKind, setEditorKind] = useState<"post" | "page" | null>(null);
  const [menu, setMenu] = useState(false); // the drawer on a phone
  const [path] = hash.split("?");
  const parts = path.split("/").filter(Boolean);

  // Going to another page closes the drawer.
  useEffect(() => setMenu(false), [hash]);

  const logout = async () => {
    if (!mayLeave()) return;
    dropUnsaved();
    try {
      await api("POST", "/auth/logout");
    } finally {
      setCsrf("");
      onLogout();
    }
  };

  let page;
  switch (parts[0]) {
    case "content":
      page = parts[1] ? <ContentEditor key={hash} me={me} idOrNew={parts[1]} query={hash.split("?")[1] ?? ""} onKind={setEditorKind} /> : <ContentList me={me} query={hash.split("?")[1] ?? ""} />;
      break;
    case "media":
      page = <MediaPage me={me} />;
      break;
    case "users":
      page = can(me, "users.manage") ? <UsersPage me={me} /> : <p className="error">Нет доступа.</p>;
      break;
    case "settings":
      page = can(me, "content.settings") ? <SettingsPage /> : <p className="error">Нет доступа.</p>;
      break;
    case "account":
      page = <Account me={me} />;
      break;
    default:
      page = (
        <>
          <div className="top">
            <h1>Здравствуйте, {me.username}</h1>
          </div>
          <p className="muted">Роль: {ROLE_RU[me.role] ?? me.role}</p>
          <div className="row" style={{ justifyContent: "flex-start" }}>
            <a className="button primary" href="#/content/new?kind=post">
              <Icon name="plus" small />
              Написать материал
            </a>
            <a className="button" href="#/content/new?kind=page">
              Создать страницу
            </a>
          </div>
        </>
      );
  }

  // Which menu entry is current: content entries depend on the kind (query or loaded item).
  const kindNow = parts[0] === "content" ? (parts[1] && parts[1] !== "new" ? editorKind : new URLSearchParams(hash.split("?")[1] ?? "").get("kind") === "page" ? "page" : "post") : null;
  const link = (to: string, label: string, icon: IconName, active: boolean) => (
    <a className={"nav" + (active ? " on" : "")} href={"#" + to} aria-current={active ? "page" : undefined}>
      <Icon name={icon} />
      {label}
    </a>
  );
  return (
    <div className="app">
      <header className="topbar">
        <button className="ghost ico" aria-label="Открыть меню" aria-expanded={menu} onClick={() => setMenu(true)}>
          <Icon name="menu" />
        </button>
        <a className="brand" href="#/" style={{ padding: 0 }}>
          <i className="dot" />
          Админка
        </a>
      </header>
      <div className={"scrim" + (menu ? " open" : "")} onClick={() => setMenu(false)} />
      <aside className={"side" + (menu ? " open" : "")} aria-label="Меню">
        <a className="brand" href="#/">
          <i className="dot" />
          Админка
        </a>
        <nav className="navs" aria-label="Разделы">
          {link("/content?kind=post", "Материалы", "doc", kindNow === "post")}
          {link("/content?kind=page", "Страницы", "page", kindNow === "page")}
          {link("/media", "Медиа", "image", parts[0] === "media")}
          {can(me, "users.manage") && link("/users", "Пользователи", "users", parts[0] === "users")}
          {can(me, "content.settings") && link("/settings", "Настройки", "gear", parts[0] === "settings")}
        </nav>
        <div className="sidefoot">
          <a className="nav" href="/" target="_blank" rel="noreferrer">
            <Icon name="ext" small />
            Открыть сайт
          </a>
          <div className="user">
            <div className="ava" aria-hidden="true">
              {me.username.slice(0, 1).toUpperCase()}
            </div>
            <a className="who" href="#/account" aria-current={parts[0] === "account" ? "page" : undefined} style={{ textDecoration: "none", color: "inherit" }}>
              {me.username}
              <small>{ROLE_RU[me.role] ?? me.role}</small>
            </a>
            <button className="ghost sm ico" aria-label="Выйти" onClick={() => void logout()}>
              <Icon name="logout" small />
            </button>
          </div>
        </div>
      </aside>
      <main className="pg">
        <div className="wrap">{page}</div>
      </main>
    </div>
  );
}
