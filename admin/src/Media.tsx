import { useCallback, useEffect, useState } from "react";
import { Icon } from "./icons";
import { altText, api, errMsg, mergeById, type Me, type MediaItem } from "./api";

const kb = (n: number) => (n < 1024 * 1024 ? (n / 1024).toFixed(0) + " КБ" : (n / 1024 / 1024).toFixed(1) + " МБ");

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
    if (done) setMsg((m) => m ?? { ok: true, text: `Загружено файлов: ${done}. Данные о съёмке, в том числе место, удалены.` });
    setBusy(false);
    load();
  };

  const remove = async (m: MediaItem) => {
    if (!confirm(`Удалить «${m.filename}»? На страницах, где она используется, останется пустое место.`)) return;
    try {
      await api("DELETE", `/media/items/${m.id}`);
      load();
    } catch (e) {
      setMsg({ ok: false, text: errMsg(e) });
    }
  };

  return (
    <>
      <div className="top">
        <h1>Медиа</h1>
        <label className="button primary file" style={{ margin: 0, minHeight: 40 }}>
          <Icon name="plus" small />
          {busy ? "Загрузка…" : "Загрузить изображения"}
          <input type="file" multiple accept="image/jpeg,image/png,image/gif,image/webp" disabled={busy} onChange={(e) => { void upload(e.target.files); e.target.value = ""; }} />
        </label>
      </div>
      <p className="muted" style={{ margin: 0 }}>JPEG, PNG, GIF или WebP, до 10 МБ каждая. Данные о съёмке (EXIF, в том числе место) удаляются при загрузке.</p>
      {msg && (
        <div className={msg.ok ? "ok-box" : "err-box"} role="alert">
          {msg.text}
        </div>
      )}
      {items && items.length === 0 && (
        <div className="empty">
          <h2>Здесь будут ваши изображения</h2>
          <p>Загрузите первую картинку: она появится в списке и будет доступна в редакторе.</p>
        </div>
      )}
      <div className="grid">
        {items?.map((m) => (
          <figure key={m.id} className="card media">
            <img src={m.url} alt={m.filename} loading="lazy" />
            <figcaption>
              <strong title={m.filename}>{m.filename}</strong>
              <span className="muted">
                {m.width}×{m.height}, {kb(m.size)}
              </span>
              <div className="row" style={{ margin: 0, gap: 8, flexWrap: "nowrap" }}>
                <CopyButton text={`![${altText(m.filename)}](${m.url})`} />
                {(m.uploader_id === me.id || me.perms?.["*"] || me.perms?.["media.manage"]) && (
                  <button className="ghost sm ico danger" aria-label={`Удалить ${m.filename}`} onClick={() => void remove(m)}>
                    <Icon name="trash" small />
                  </button>
                )}
              </div>
            </figcaption>
          </figure>
        ))}
      </div>
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

// CopyButton puts the Markdown for an image on the clipboard.
function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 2000);
    } catch {
      /* clipboard blocked: the user can still insert the image from the editor */
    }
  };
  return (
    <button type="button" className="sm" style={{ flex: "1 1 auto" }} onClick={() => void copy()}>
      {done ? "Скопировано" : "Скопировать для текста"}
    </button>
  );
}
