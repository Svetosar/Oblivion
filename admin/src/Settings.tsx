import { useEffect, useState, type FormEvent } from "react";
import { api, errMsg } from "./api";

interface SiteSettings {
  site_title: string;
  site_description: string;
  base_url: string;
  content_lang: string;
}

export function SettingsPage() {
  const [s, setS] = useState<SiteSettings | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => {
    api<SiteSettings>("GET", "/content/settings").then(setS, (e) => setMsg({ ok: false, text: errMsg(e) }));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!s) return;
    setMsg(null);
    try {
      setS(await api<SiteSettings>("PUT", "/content/settings", s));
      setMsg({ ok: true, text: "Saved." });
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  if (!s) return msg ? <p className="error">{msg.text}</p> : <p className="muted">Loading…</p>;
  const set = (k: keyof SiteSettings) => (e: { target: { value: string } }) => setS({ ...s, [k]: e.target.value });
  return (
    <>
      <h1>Site settings</h1>
      {!s.base_url && (
        <p className="warn" role="status">
          The public address is not set. Without it the site cannot publish canonical links, a sitemap or an RSS feed
          (search engines need absolute URLs).
        </p>
      )}
      <form className="card" onSubmit={(e) => void save(e)}>
        <label>
          Site title (empty = default)
          <input value={s.site_title} onChange={set("site_title")} maxLength={100} />
        </label>
        <label>
          Description (shown on the front page and in search results)
          <textarea value={s.site_description} onChange={set("site_description")} rows={3} maxLength={300} />
        </label>
        <label>
          Public address, for example https://example.com
          <input value={s.base_url} onChange={set("base_url")} placeholder="https://example.com" inputMode="url" />
        </label>
        <label>
          Language of your posts (code, for example ru or en)
          <input value={s.content_lang} onChange={set("content_lang")} maxLength={11} />
        </label>
        {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
        <button className="primary">Save</button>
      </form>
    </>
  );
}
