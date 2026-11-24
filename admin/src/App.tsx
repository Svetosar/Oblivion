import { useCallback, useEffect, useState } from "react";
import { ApiError, api, can, errMsg, onUnauthorized, setCsrf, type Session } from "./api";
import { go, useHash } from "./hooks";
import { Login, Setup } from "./Auth";
import { ContentEditor, ContentList } from "./Content";
import { MediaPage } from "./Media";
import { UsersPage } from "./Users";
import { Account } from "./Account";
import { SettingsPage } from "./Settings";

type State = "loading" | "login" | "setup" | { error: string } | Session;

export function App() {
  const [state, setState] = useState<State>("loading");

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
    onUnauthorized(() => setState("login"));
    void probe();
  }, [probe]);

  const entered = (s: Session) => {
    setCsrf(s.csrf);
    // Refresh to get the permission map, which login/setup responses do not carry.
    void probe();
    go("/");
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
  return <Shell session={state} onLogout={() => setState("login")} />;
}

function Shell({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const hash = useHash();
  const me = session.user;
  const [path] = hash.split("?");
  const parts = path.split("/").filter(Boolean);

  const logout = async () => {
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
      page = parts[1] ? <ContentEditor key={hash} me={me} idOrNew={parts[1]} query={hash.split("?")[1] ?? ""} /> : <ContentList me={me} query={hash.split("?")[1] ?? ""} />;
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

  const link = (to: string, label: string) => (
    <a href={"#" + to} className={parts[0] === to.split("/")[1] ? "active" : ""}>
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
          {link("/content?kind=post", "Posts")}
          {link("/content?kind=page", "Pages")}
          {link("/media", "Media")}
          {can(me, "users.manage") && link("/users", "Users")}
          {can(me, "content.settings") && link("/settings", "Settings")}
          {link("/account", "Account")}
          <a href="/" target="_blank" rel="noreferrer">
            View site
          </a>
        </nav>
        <button className="link" onClick={() => void logout()}>
          Log out ({me.username})
        </button>
      </header>
      <main>{page}</main>
    </>
  );
}
