import { useCallback, useEffect, useState } from "react";
import { ApiError, api, can, errMsg, onUnauthorized, setCsrf, type Session } from "./api";
import { dropUnsaved, go, hasUnsaved, mayLeave, useHash } from "./hooks";
import { Login, Setup } from "./Auth";
import { ContentEditor, ContentList } from "./Content";
import { MediaPage } from "./Media";
import { UsersPage } from "./Users";
import { Account } from "./Account";
import { SettingsPage } from "./Settings";

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

  if (state === "loading") return <p className="center muted">Loading…</p>;
  if (typeof state === "object" && "error" in state)
    return (
      <div className="center">
        <p className="error">Cannot reach the server: {state.error}</p>
        <button onClick={() => void probe()}>Retry</button>
      </div>
    );
  if (state === "setup") return <Setup onDone={entered} />;
  if (state === "login") return <Login onDone={entered} />;
  const userId = state.user.id;
  return (
    <>
      {expired && <Login note="Your session ended. Sign in again; unsaved text is kept." onDone={(s) => void reentered(userId, s)} />}
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
  const [path] = hash.split("?");
  const parts = path.split("/").filter(Boolean);

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
      page = can(me, "users.manage") ? <UsersPage me={me} /> : <p className="error">Not allowed.</p>;
      break;
    case "settings":
      page = can(me, "content.settings") ? <SettingsPage /> : <p className="error">Not allowed.</p>;
      break;
    case "account":
      page = <Account me={me} />;
      break;
    default:
      page = (
        <>
          <h1>Welcome, {me.username}</h1>
          <p className="muted">Role: {me.role}</p>
          <p>
            <a href="#/content/new?kind=post">Write a post</a> · <a href="#/content/new?kind=page">Create a page</a>
          </p>
        </>
      );
  }

  // Which menu entry is current: content entries depend on the kind (query or loaded item).
  const kindNow = parts[0] === "content" ? (parts[1] && parts[1] !== "new" ? editorKind : new URLSearchParams(hash.split("?")[1] ?? "").get("kind") === "page" ? "page" : "post") : null;
  const link = (to: string, label: string, active: boolean) => (
    <a href={"#" + to} className={active ? "active" : undefined} aria-current={active ? "page" : undefined}>
      {label}
    </a>
  );
  return (
    <>
      <header className="bar">
        <a className="brand" href="#/">
          Admin
        </a>
        <nav>
          {link("/content?kind=post", "Posts", kindNow === "post")}
          {link("/content?kind=page", "Pages", kindNow === "page")}
          {link("/media", "Media", parts[0] === "media")}
          {can(me, "users.manage") && link("/users", "Users", parts[0] === "users")}
          {can(me, "content.settings") && link("/settings", "Settings", parts[0] === "settings")}
          {link("/account", "Account", parts[0] === "account")}
        </nav>
        <a className="site-link" href="/" target="_blank" rel="noreferrer">
          View site
        </a>
        <button className="link" onClick={() => void logout()}>
          Log out ({me.username})
        </button>
      </header>
      <main>{page}</main>
    </>
  );
}
