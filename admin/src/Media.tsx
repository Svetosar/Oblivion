import { useCallback, useEffect, useState } from "react";
import { altText, api, errMsg, mergeById, type Me, type MediaItem } from "./api";

const kb = (n: number) => (n < 1024 * 1024 ? (n / 1024).toFixed(0) + " KB" : (n / 1024 / 1024).toFixed(1) + " MB");

const PAGE = 48;

export function MediaPage({ me }: { me: Me }) {
  const [items, setItems] = useState<MediaItem[] | null>(null);
  const [more, setMore] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api<MediaItem[]>("GET", `/media/items?limit=${PAGE}`).then(
      (r) => {
        setItems(r);
        setMore(r.length === PAGE);
      },
      (e) => setMsg({ ok: false, text: errMsg(e) }),
    );
  }, []);
  useEffect(load, [load]);
  const showMore = () => {
    if (!items) return;
    api<MediaItem[]>("GET", `/media/items?limit=${PAGE}&offset=${items.length}`).then(
      (r) => {
        setItems((cur) => mergeById(cur ?? [], r));
        setMore(r.length === PAGE);
      },
      (e) => setMsg({ ok: false, text: errMsg(e) }),
    );
  };

  const upload = async (files: FileList | null) => {
    if (!files?.length) return;
    setBusy(true);
    setMsg(null);
    let done = 0;
    for (const f of Array.from(files)) {
      const fd = new FormData();
      fd.append("file", f);
      try {
        await api("POST", "/media/items", fd);
        done++;
      } catch (e) {
        setMsg({ ok: false, text: `${f.name}: ${errMsg(e)}` });
      }
    }
    if (done) setMsg((m) => m ?? { ok: true, text: `Uploaded ${done} file(s). Location data and other metadata were removed.` });
    setBusy(false);
    load();
  };

  const remove = async (m: MediaItem) => {
    if (!confirm(`Delete ${m.filename}? Pages that use it will show a broken image.`)) return;
    try {
      await api("DELETE", `/media/items/${m.id}`);
      load();
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  return (
    <>
      <h1>Media</h1>
      <label className="button primary file">
        {busy ? "Uploading…" : "Upload images"}
        <input type="file" multiple accept="image/jpeg,image/png,image/gif,image/webp" disabled={busy} onChange={(e) => { void upload(e.target.files); e.target.value = ""; }} />
      </label>
      <p className="muted">JPEG, PNG, GIF or WebP, up to 10 MiB each.</p>
      {msg && <p className={msg.ok ? "ok" : "error"} role="alert">{msg.text}</p>}
      {items && items.length === 0 && <p className="muted">No images yet.</p>}
      <div className="grid">
        {items?.map((m) => (
          <figure key={m.id} className="card media">
            <img src={m.url} alt={m.filename} loading="lazy" />
            <figcaption>
              <strong title={m.filename}>{m.filename}</strong>
              <span className="muted">
                {m.width}×{m.height} · {kb(m.size)}
              </span>
              <input readOnly value={`![${altText(m.filename)}](${m.url})`} onFocus={(e) => e.target.select()} aria-label="Markdown" />
              {(m.uploader_id === me.id || me.perms?.["*"] || me.perms?.["media.manage"]) && (
                <button className="danger" onClick={() => void remove(m)}>
                  Delete
                </button>
              )}
            </figcaption>
          </figure>
        ))}
      </div>
      {more && (
        <p>
          <button type="button" onClick={showMore}>
            Show more
          </button>
        </p>
      )}
    </>
  );
}
