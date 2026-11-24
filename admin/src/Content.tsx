import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, can, errMsg, fmtDate, type Item, type MediaItem, type Me } from "./api";
import { go } from "./hooks";

export function ContentList({ me, query }: { me: Me; query: string }) {
  const kind = new URLSearchParams(query).get("kind") === "page" ? "page" : "post";
  const [items, setItems] = useState<Item[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    setItems(null);
    api<Item[]>("GET", `/content/items?kind=${kind}&limit=200`).then(setItems, (e) => setErr(errMsg(e)));
  }, [kind]);
  const canWrite = can(me, "content.write") || can(me, "content.write.own");
  return (
    <>
      <div className="row">
        <h1>{kind === "post" ? "Posts" : "Pages"}</h1>
        {canWrite && (
          <a className="button primary" href={`#/content/new?kind=${kind}`}>
            New {kind}
          </a>
        )}
      </div>
      {err && <p className="error">{err}</p>}
      {items === null && !err && <p className="muted">Loading…</p>}
      {items && items.length === 0 && <p className="muted">Nothing here yet.</p>}
      {items && items.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Title</th>
              <th>Status</th>
              <th>Author</th>
              <th>Updated</th>
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.id}>
                <td>
                  <a href={`#/content/${it.id}`}>{it.title}</a>
                </td>
                <td>
                  <span className={"tag " + it.status}>{it.status}</span>
                </td>
                <td>{it.author_name}</td>
                <td>{fmtDate(it.updated_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

export function ContentEditor({ me, idOrNew, query, onKind }: { me: Me; idOrNew: string; query: string; onKind: (k: "post" | "page") => void }) {
  const isNew = idOrNew === "new";
  const [kind, setKind] = useState<"post" | "page">(new URLSearchParams(query).get("kind") === "page" ? "page" : "post");
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [body, setBody] = useState("");
  const [status, setStatus] = useState<"draft" | "published">("draft");
  const [loaded, setLoaded] = useState(isNew);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [preview, setPreview] = useState("");
  const [picker, setPicker] = useState(false);
  const [tab, setTab] = useState<"write" | "preview">("write"); // small screens show one pane at a time
  const ta = useRef<HTMLTextAreaElement>(null);

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

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    try {
      if (isNew) {
        const it = await api<Item>("POST", "/content/items", { kind, title, slug, body_md: body, status });
        go(`/content/${it.id}`);
      } else {
        const it = await api<Item>("PUT", `/content/items/${idOrNew}`, { title, slug, body_md: body, status });
        setSlug(it.slug);
        setMsg({ ok: true, text: "Saved." });
      }
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  const remove = async () => {
    if (!confirm("Delete this permanently?")) return;
    try {
      await api("DELETE", `/content/items/${idOrNew}`);
      go(`/content?kind=${kind}`);
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  const insert = (m: MediaItem) => {
    const md = `![${m.filename.replace(/[[\]]/g, "")}](${m.url})`;
    const el = ta.current;
    const at = el ? el.selectionStart : body.length;
    setBody(body.slice(0, at) + md + body.slice(at));
    setPicker(false);
    el?.focus();
  };

  if (!loaded) return msg ? <p className="error">{msg.text}</p> : <p className="muted">Loading…</p>;
  return (
    <form onSubmit={(e) => void save(e)}>
      <div className="row">
        <h1>
          {isNew ? "New" : "Edit"} {kind}
        </h1>
        <a href={`#/content?kind=${kind}`}>← Back</a>
      </div>
      <label>
        Title
        <input value={title} onChange={(e) => setTitle(e.target.value)} required maxLength={200} />
      </label>
      <label>
        URL slug (leave empty to generate from the title)
        <input value={slug} onChange={(e) => setSlug(e.target.value)} pattern="[a-z0-9]+(-[a-z0-9]+)*" placeholder="my-post" />
      </label>
      <div className="row">
        <div className="tabs" role="tablist" aria-label="Editor view">
          <button type="button" role="tab" aria-selected={tab === "write"} className={tab === "write" ? "on" : undefined} onClick={() => setTab("write")}>
            Write
          </button>
          <button type="button" role="tab" aria-selected={tab === "preview"} className={tab === "preview" ? "on" : undefined} onClick={() => setTab("preview")}>
            Preview
          </button>
        </div>
        <span className="desktop-only">Markdown</span>
        <button type="button" onClick={() => setPicker(!picker)}>
          Insert image
        </button>
      </div>
      {picker && <MediaPicker onPick={insert} />}
      <div className={"editor tab-" + tab}>
        <textarea ref={ta} value={body} onChange={(e) => setBody(e.target.value)} rows={22} spellCheck aria-label="Markdown" />
        {/* The HTML below was produced and sanitized by the server (goldmark + bluemonday). */}
        <div className="preview body" dangerouslySetInnerHTML={{ __html: preview }} />
      </div>
      <div className="row">
        <label className="inline">
          Status{" "}
          <select value={status} onChange={(e) => setStatus(e.target.value as "draft" | "published")}>
            <option value="draft">Draft</option>
            <option value="published" disabled={!canPublish}>
              Published
            </option>
          </select>
        </label>
        {!canPublish && <span className="muted">Your role cannot publish; an editor must do that.</span>}
        <span className="spacer" />
        {!isNew && (
          <button type="button" className="danger" onClick={() => void remove()}>
            Delete
          </button>
        )}
        <button className="primary">Save</button>
      </div>
      {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
    </form>
  );
}

function MediaPicker({ onPick }: { onPick: (m: MediaItem) => void }) {
  const [items, setItems] = useState<MediaItem[] | null>(null);
  useEffect(() => {
    api<MediaItem[]>("GET", "/media/items?limit=24").then(setItems, () => setItems([]));
  }, []);
  if (!items) return <p className="muted">Loading…</p>;
  if (items.length === 0) return <p className="muted">No images yet. Upload some in Media.</p>;
  return (
    <div className="grid picker">
      {items.map((m) => (
        <button type="button" key={m.id} className="thumb" onClick={() => onPick(m)} title={m.filename}>
          <img src={m.url} alt={m.filename} loading="lazy" />
        </button>
      ))}
    </div>
  );
}
