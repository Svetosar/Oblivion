import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, errMsg } from "./api";
import { useUnsavedGuard } from "./hooks";
import { Icon } from "./icons";

interface SiteSettings {
  site_title: string;
  site_description: string;
  base_url: string;
  content_lang: string;
}

export function SettingsPage() {
  const [s, setS] = useState<SiteSettings | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const saved = useRef(""); // what the server holds, as JSON
  useUnsavedGuard(() => s !== null && JSON.stringify(s) !== saved.current);
  useEffect(() => {
    api<SiteSettings>("GET", "/content/settings").then(
      (r) => {
        saved.current = JSON.stringify(r);
        setS(r);
      },
      (e) => setMsg({ ok: false, text: errMsg(e) }),
    );
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!s) return;
    setMsg(null);
    try {
      const r = await api<SiteSettings>("PUT", "/content/settings", s);
      saved.current = JSON.stringify(r);
      setS(r);
      setMsg({ ok: true, text: "Сохранено." });
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  if (!s) return msg ? <p className="error">{msg.text}</p> : <p className="muted">Загрузка…</p>;
  const set = (k: keyof SiteSettings) => (e: { target: { value: string } }) => setS({ ...s, [k]: e.target.value });
  return (
    <>
      <div className="top">
        <h1>Настройки сайта</h1>
      </div>
      {!s.base_url && (
        <div className="warn" role="status">
          <Icon name="alert" />
          <div>
            <div style={{ fontWeight: 600 }}>Не задан адрес сайта</div>
            <div>Без него сайт не может выдавать канонические ссылки, карту сайта и RSS-ленту: поисковикам нужны полные адреса.</div>
          </div>
        </div>
      )}
      <form className="card" onSubmit={(e) => void save(e)} style={{ maxWidth: "46rem", padding: "var(--space-8)" }}>
        <label style={{ marginTop: 0 }}>
          Название сайта
          <input value={s.site_title} onChange={set("site_title")} maxLength={100} />
          <span className="help">Пусто: используется название по умолчанию</span>
        </label>
        <label>
          Описание
          <textarea value={s.site_description} onChange={set("site_description")} rows={3} maxLength={300} />
          <span className="help">Показывается на главной и в результатах поиска · {s.site_description.length} из 300 символов</span>
        </label>
        <label>
          Адрес сайта
          <input value={s.base_url} onChange={set("base_url")} placeholder="https://example.com" inputMode="url" />
          <span className="help">Например, https://example.com. Нужен для карты сайта, RSS и канонических ссылок</span>
        </label>
        <label>
          Язык материалов
          <input value={s.content_lang} onChange={set("content_lang")} maxLength={11} style={{ maxWidth: "14rem" }} />
          <span className="help">Код языка, например ru или en</span>
        </label>
        {msg && (
          <div className={msg.ok ? "ok-box" : "err-box"} role="alert" style={{ marginTop: 16 }}>
            {msg.text}
          </div>
        )}
        <div style={{ marginTop: 24 }}>
          <button className="primary">Сохранить настройки</button>
        </div>
      </form>
    </>
  );
}
