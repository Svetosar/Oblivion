let csrf = "";
export const setCsrf = (t: string) => {
  csrf = t;
};

let unauthorized: () => void = () => {};
export const onUnauthorized = (fn: () => void) => {
  unauthorized = fn;
};

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  let payload: BodyInit | undefined;
  if (body instanceof FormData) {
    payload = body;
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  if (method !== "GET") headers["X-CSRF-Token"] = csrf;
  const res = await fetch("/api/v1" + path, { method, headers, body: payload, credentials: "same-origin" });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    // A 401 on anything but the login/setup/me probes means the session ended.
    if (res.status === 401 && !path.startsWith("/auth/")) unauthorized();
    throw new ApiError(res.status, (data && data.error) || res.statusText);
  }
  return data as T;
}

export interface Me {
  id: number;
  username: string;
  role: string;
  perms?: Record<string, boolean>;
}
export interface Session {
  user: Me;
  csrf: string;
}
export interface Item {
  id: number;
  kind: "post" | "page";
  slug: string;
  title: string;
  body_md?: string;
  status: "draft" | "published";
  version: number; // counts saves; updates must send the one that was loaded
  byline: string; // the author's nickname shown on the site; the login (author_name) is never public
  author_name: string;
  updated_at: number;
  published_at: number | null;
}
export interface MediaItem {
  id: number;
  url: string;
  filename: string;
  mime: string;
  size: number;
  width: number;
  height: number;
  uploader_id: number;
}
export interface User {
  id: number;
  username: string;
  role: string;
  disabled: boolean;
  created_at: number;
}

// Alt text proposed for an uploaded image: the file name without its extension.
// It is only a start: the text stays editable in the editor.
export const altText = (filename: string) => filename.replace(/\.[^.]+$/, "").replace(/[[\]]/g, "").trim() || "изображение";

// mergeById appends a fetched page to what is shown, skipping rows that moved
// between pages while the list was open.
export function mergeById<T extends { id: number }>(have: T[], more: T[]): T[] {
  const seen = new Set(have.map((x) => x.id));
  return have.concat(more.filter((x) => !seen.has(x.id)));
}

export const can = (me: Me, perm: string) => !!(me.perms && (me.perms["*"] || me.perms[perm]));
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));
export const ROLE_RU: Record<string, string> = { admin: "администратор", editor: "редактор", author: "автор" };

// fmtDate shows "сегодня, 09:40", "вчера, 21:10", or "24 ноября" (with the year for another year).
export function fmtDate(ts: number | null): string {
  if (!ts) return "—";
  const d = new Date(ts * 1000);
  const now = new Date();
  const day = (x: Date) => Math.floor(new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime() / 86400000);
  const time = d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
  if (day(d) === day(now)) return `сегодня, ${time}`;
  if (day(d) === day(now) - 1) return `вчера, ${time}`;
  const date = d.toLocaleDateString("ru-RU", d.getFullYear() === now.getFullYear() ? { day: "numeric", month: "long" } : { day: "numeric", month: "long", year: "numeric" });
  return date;
}
