import { useEffect, useRef, useState, type FormEvent } from "react";
import { ApiError, altText, api, can, errMsg, fmtDate, mergeById, type Item, type MediaItem, type Me } from "./api";
import { go, useUnsavedGuard } from "./hooks";
import { Icon } from "./icons";

const LIST_PAGE = 50;

export function ContentList({ me, query }: { me: Me; query: string }) {
  const kind = new URLSearchParams(query).get("kind") === "page" ? "page" : "post";
  const [items, setItems] = useState<Item[] | null>(null);
  const [more, setMore] = useState(false);
  const [err, setErr] = useState("");
  useEffect(() => {
    setItems(null);
    setMore(false);
    setErr("");
    api<Item[]>("GET", `/content/items?kind=${kind}&limit=${LIST_PAGE}`).then(
      (r) => {
        setItems(r);
        setMore(r.length === LIST_PAGE);
      },
      (e) => setErr(errMsg(e)),
    );
  }, [kind]);
  const showMore = () => {
    if (!items) return;
    api<Item[]>("GET", `/content/items?kind=${kind}&limit=${LIST_PAGE}&offset=${items.length}`).then(
      (r) => {
        setItems((cur) => mergeById(cur ?? [], r));
        setMore(r.length === LIST_PAGE);
      },
      (e) => setErr(errMsg(e)),
    );
  };
  const canWrite = can(me, "content.write") || can(me, "content.write.own");
  const noun = kind === "post" ? "материал" : "страницу";
  return (
    <>
      <div className="top">
        <h1>{kind === "post" ? "Материалы" : "Страницы"}</h1>
        {canWrite && (
          <a className="button primary" href={`#/content/new?kind=${kind}`}>
            <Icon name="plus" small />
            {kind === "post" ? "Написать материал" : "Создать страницу"}
          </a>
        )}
      </div>
      {err && (
        <div className="err-box" role="alert">
          Не удалось загрузить список: {err}
        </div>
      )}
      {items === null && !err && (
        <div className="card" role="status" aria-busy="true" aria-label="Загрузка списка" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <div className="skel" style={{ height: 18, width: "60%" }} />
          <div className="skel" style={{ height: 18, width: "75%" }} />
          <div className="skel" style={{ height: 18, width: "50%" }} />
        </div>
      )}
      {items && items.length === 0 && (
        <div className="empty">
          <h2>{kind === "post" ? "Здесь появятся ваши материалы" : "Здесь появятся ваши страницы"}</h2>
          <p>Создайте первую {noun === "материал" ? "запись" : "страницу"}: она сохранится как черновик, пока вы её не опубликуете.</p>
          {canWrite && (
            <a className="button primary" href={`#/content/new?kind=${kind}`}>
              <Icon name="plus" small />
              {kind === "post" ? "Написать материал" : "Создать страницу"}
            </a>
          )}
        </div>
      )}
      {items && items.length > 0 && (
        <table className="stack">
          <thead>
            <tr>
              <th>{kind === "post" ? "Материал" : "Страница"}</th>
              <th>Статус</th>
              <th>Автор</th>
              <th>Изменён</th>
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.id}>
                <td className="wide">
                  <a href={`#/content/${it.id}`}>{it.title}</a>
                </td>
                <td>
                  <span className={"tag " + it.status}>
                    <Icon name={it.status === "published" ? "check" : "pencil"} small />
                    {it.status === "published" ? "Опубликовано" : "Черновик"}
                  </span>
                </td>
                <td>{it.author_name}</td>
                <td>{fmtDate(it.updated_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {items && items.length > 0 && (
        <p className="muted" style={{ textAlign: "center", margin: 0 }} role="status">
          Показано: {items.length}
        </p>
      )}
      {more && (
        <div className="row" style={{ justifyContent: "center" }}>
          <button type="button" onClick={showMore}>
            Показать ещё
          </button>
        </div>
      )}
    </>
  );
}

// Everything Save sends, as one comparable string.
const snapshot = (title: string, slug: string, body: string, status: string, byline: string) => JSON.stringify([title, slug, body, status, byline]);

// The nickname last used, offered again for the next article. Only a convenience:
// storage may be blocked, and the server decides what is acceptable.
const lastByline = {
  get: () => {
    try {
      return localStorage.getItem("byline") ?? "";
    } catch {
      return "";
    }
  },
  set: (v: string) => {
    try {
      localStorage.setItem("byline", v);
    } catch {
      /* ignore */
    }
  },
};

export function ContentEditor({ me, idOrNew, query, onKind }: { me: Me; idOrNew: string; query: string; onKind: (k: "post" | "page") => void }) {
  const isNew = idOrNew === "new";
  const [kind, setKind] = useState<"post" | "page">(new URLSearchParams(query).get("kind") === "page" ? "page" : "post");
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [body, setBody] = useState("");
  const [status, setStatus] = useState<"draft" | "published">("draft");
  const [byline, setByline] = useState(() => (isNew && new URLSearchParams(query).get("kind") !== "page" ? lastByline.get() : ""));
  const [loaded, setLoaded] = useState(isNew);
  // The address the item has on the site now; changing it breaks links to a published item.
  const [live, setLive] = useState<{ slug: string; published: boolean } | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [preview, setPreview] = useState("");
  const [picker, setPicker] = useState(false);
  const [tab, setTab] = useState<"write" | "preview">("write");
  const ta = useRef<HTMLTextAreaElement>(null);
  const [saving, setSaving] = useState(false);
  const version = useRef(0); // the version of the item that was loaded or last saved
  const [stale, setStale] = useState(false); // the server holds a newer version than ours
  const busy = useRef(false); // set at once, unlike state: blocks a second submit
  // What the server holds. Refs, so that navigation right after Save sees the new value.
  const stored = useRef(snapshot("", "", "", "draft", isNew ? byline : ""));
  const draft = snapshot(title, slug, body, status, byline);
  const dirty = draft !== stored.current;
  useUnsavedGuard(() => draft !== stored.current);

  useEffect(() => {
    onKind(kind);
  }, [kind, onKind]);

  useEffect(() => {
    if (isNew) return;
    api<Item>("GET", `/content/items/${idOrNew}`).then(
      (it) => {
        setKind(it.kind);
        setTitle(it.title);
        setSlug(it.slug);
        setBody(it.body_md ?? "");
        setStatus(it.status);
        setByline(it.byline ?? "");
        setLive({ slug: it.slug, published: it.status === "published" });
        stored.current = snapshot(it.title, it.slug, it.body_md ?? "", it.status, it.byline ?? "");
        version.current = it.version;
        setLoaded(true);
      },
      (e) => setMsg({ ok: false, text: errMsg(e) }),
    );
  }, [idOrNew, isNew]);

  // Server-side preview: exactly the sanitizer that publishing uses.
  useEffect(() => {
    const t = setTimeout(() => {
      api<{ html: string }>("POST", "/content/preview", { body_md: body }).then(
        (r) => setPreview(r.html),
        () => {},
      );
    }, 400);
    return () => clearTimeout(t);
  }, [body]);

  const canPublish = can(me, "content.publish");

  // save stores the item; `st` lets the Publish button save with the status "published" in one step.
  const save = async (e?: FormEvent, st: "draft" | "published" = status) => {
    e?.preventDefault();
    if (busy.current) return;
    busy.current = true;
    setSaving(true); // fields are read-only meanwhile, so nothing typed gets lost
    setMsg(null);
    setStale(false);
    const now = snapshot(title, slug, body, st, byline);
    try {
      if (isNew) {
        const it = await api<Item>("POST", "/content/items", { kind, title, slug, body_md: body, status: st, byline });
        if (byline) lastByline.set(byline);
        stored.current = now;
        go(`/content/${it.id}`);
        return; // stay busy until the editor of the new item replaces this one
      }
      const it = await api<Item>("PUT", `/content/items/${idOrNew}`, { title, slug, body_md: body, status: st, byline, version: version.current });
      version.current = it.version;
      if (byline) lastByline.set(byline);
      stored.current = snapshot(title, it.slug, body, st, byline);
      setStatus(st);
      setSlug(it.slug);
      setLive({ slug: it.slug, published: it.status === "published" });
      setMsg({ ok: true, text: st === "published" && status !== "published" ? "Опубликовано." : "Сохранено." });
    } catch (e) {
      // 412: someone saved this item after we opened it. Our text stays in the editor.
      setStale(e instanceof ApiError && e.status === 412);
      setMsg({ ok: false, text: errMsg(e) });
    }
    busy.current = false;
    setSaving(false);
  };

  // Save anyway: adopt the server's current version, then save our text over it.
  const overwrite = async () => {
    try {
      version.current = (await api<Item>("GET", `/content/items/${idOrNew}`)).version;
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
      return;
    }
    await save();
  };

  const remove = async () => {
    if (!confirm(`Удалить «${title || "без названия"}»? Это нельзя отменить.`)) return;
    try {
      await api("DELETE", `/content/items/${idOrNew}`);
      stored.current = draft; // already confirmed: do not ask again
      go(`/content?kind=${kind}`);
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  const insert = (m: MediaItem) => {
    if (busy.current) return;
    const md = `![${altText(m.filename)}](${m.url})`;
    const el = ta.current;
    const at = el ? el.selectionStart : body.length;
    setBody(body.slice(0, at) + md + body.slice(at));
    setPicker(false);
    el?.focus();
  };

  if (!loaded) return msg ? <div className="err-box" role="alert">{msg.text}</div> : <p className="muted">Загрузка…</p>;
  const noun = kind === "post" ? "материал" : "страницу";
  return (
    <form onSubmit={(e) => void save(e)}>
      <div className="ed-bar">
        <a className="button ghost sm" href={`#/content?kind=${kind}`} style={{ paddingLeft: 8 }}>
          <Icon name="left" small />
          {kind === "post" ? "Материалы" : "Страницы"}
        </a>
        <span className={"tag " + status}>
          <Icon name={status === "published" ? "check" : "pencil"} small />
          {status === "published" ? "Опубликовано" : "Черновик"}
        </span>
        <span className="muted" role="status" style={{ fontSize: "var(--text-sm)" }}>
          {saving ? "Сохранение…" : dirty ? "Изменения не сохранены" : msg?.ok ? msg.text : isNew ? "Новый " + (kind === "post" ? "материал" : "страница") : "Все изменения сохранены"}
        </span>
        <span className="spacer" />
        <button type="button" className="sm" aria-pressed={tab === "preview"} onClick={() => setTab(tab === "preview" ? "write" : "preview")}>
          <Icon name="eye" small />
          {tab === "preview" ? "Назад к тексту" : "Предпросмотр"}
        </button>
        <button type="submit" className="sm" aria-disabled={saving}>
          Сохранить
        </button>
        {canPublish && status !== "published" && (
          <button type="button" className="primary sm" aria-disabled={saving} onClick={() => void save(undefined, "published")}>
            Опубликовать
          </button>
        )}
        {canPublish && status === "published" && (
          <button type="button" className="sm" aria-disabled={saving} onClick={() => void save(undefined, "draft")}>
            Снять с публикации
          </button>
        )}
      </div>

      {msg && !msg.ok && (
        <div className="err-box" role="alert">
          {msg.text}
        </div>
      )}
      {stale && (
        <div className="warn" role="alert" style={{ flexDirection: "column" }}>
          <div style={{ fontWeight: 600 }}>Материал уже изменили, пока вы его правили</div>
          <div>Ваш текст остался в редакторе. Выберите, какую версию оставить.</div>
          <div className="row" style={{ margin: 0, justifyContent: "flex-start" }}>
            <button type="button" className="primary sm" onClick={() => void overwrite()}>
              Сохранить мой текст поверх
            </button>
            {/* Reloading asks first, because the text in this editor would be lost. */}
            <button type="button" className="sm" onClick={() => location.reload()}>
              Загрузить новую версию
            </button>
          </div>
        </div>
      )}

      <div className="ed">
        <div className="ed-main">
          <div className="ed-col">
            <label style={{ margin: 0 }}>
              <span className="sr-only">Заголовок</span>
              <textarea
                className="ed-title"
                rows={1}
                value={title}
                onChange={(e) => setTitle(e.target.value.replace(/\n/g, " "))}
                onKeyDown={(e) => e.key === "Enter" && e.preventDefault()}
                readOnly={saving}
                required
                maxLength={200}
                placeholder="Заголовок"
              />
            </label>
            <div className="row" style={{ margin: 0, justifyContent: "flex-start" }}>
              <button type="button" className="sm" onClick={() => setPicker(!picker)}>
                <Icon name="image" small />
                Вставить картинку
              </button>
              <span className="help">Текст пишется в Markdown</span>
            </div>
            {picker && <MediaPicker onPick={insert} />}
            <div className={"editor tab-" + tab}>
              <textarea ref={ta} value={body} onChange={(e) => setBody(e.target.value)} readOnly={saving} rows={22} spellCheck aria-label="Текст в Markdown" placeholder={"Начните писать " + noun + "…"} />
              {/* The HTML below was produced and sanitized by the server (goldmark + bluemonday). */}
              <div className="preview body" dangerouslySetInnerHTML={{ __html: preview }} />
            </div>
          </div>
        </div>

        <aside className="ed-panel" aria-label="Публикация">
          <section className="card" style={{ display: "flex", flexDirection: "column" }}>
            <h2>Публикация</h2>
            <label className="inline" style={{ marginTop: 0, justifyContent: "space-between" }}>
              Статус
              <select value={status} disabled={saving} onChange={(e) => setStatus(e.target.value as "draft" | "published")}>
                <option value="draft">Черновик</option>
                <option value="published" disabled={!canPublish}>
                  Опубликовано
                </option>
              </select>
            </label>
            {!canPublish && <p className="help" style={{ margin: "8px 0 0" }}>Ваша роль не позволяет публиковать: это делает редактор.</p>}
            <label>
              Адрес
              <input value={slug} onChange={(e) => setSlug(e.target.value)} readOnly={saving} pattern="[a-z0-9]+(-[a-z0-9]+)*" placeholder="my-post" />
              <span className="help">Пусто: адрес создаётся из заголовка. Латиница, цифры и дефис</span>
            </label>
            {live?.published && slug !== "" && slug !== live.slug && (
              <div className="warn" role="status" style={{ marginBottom: 0 }}>
                Материал уже опубликован. Если сменить адрес, сломаются все ссылки на него, в том числе в поисковиках.
              </div>
            )}
            {kind === "post" && (
              <label>
                Автор на сайте
                <input value={byline} onChange={(e) => setByline(e.target.value)} readOnly={saving} pattern="[A-Za-z0-9][A-Za-z0-9 ._\-]{1,31}" maxLength={32} placeholder="Marina K." autoComplete="off" />
                <span className="help">Латиница, цифры, пробел, точка. Пусто: автор не показывается</span>
              </label>
            )}
          </section>
          {!isNew && (
            <button type="button" className="ghost danger" onClick={() => void remove()}>
              <Icon name="trash" small />
              Удалить {kind === "post" ? "материал" : "страницу"}
            </button>
          )}
        </aside>
      </div>
    </form>
  );
}

const PICKER_PAGE = 24;

function MediaPicker({ onPick }: { onPick: (m: MediaItem) => void }) {
  const [items, setItems] = useState<MediaItem[] | null>(null);
  const [more, setMore] = useState(false);
  useEffect(() => {
    api<MediaItem[]>("GET", `/media/items?limit=${PICKER_PAGE}`).then(
      (r) => {
        setItems(r);
        setMore(r.length === PICKER_PAGE);
      },
      () => setItems([]),
    );
  }, []);
  const showMore = () => {
    if (!items) return;
    api<MediaItem[]>("GET", `/media/items?limit=${PICKER_PAGE}&offset=${items.length}`).then(
      (r) => {
        setItems((cur) => mergeById(cur ?? [], r));
        setMore(r.length === PICKER_PAGE);
      },
      () => setMore(false),
    );
  };
  if (!items) return <p className="muted">Загрузка…</p>;
  if (items.length === 0) return <p className="muted">Картинок пока нет. Загрузите их в разделе «Медиа».</p>;
  return (
    <>
      <div className="grid picker">
        {items.map((m) => (
          <button type="button" key={m.id} className="thumb" onClick={() => onPick(m)} title={m.filename}>
            <img src={m.url} alt={m.filename} loading="lazy" />
          </button>
        ))}
      </div>
      {more && (
        <p>
          <button type="button" onClick={showMore}>
            Показать ещё
          </button>
        </p>
      )}
    </>
  );
}
