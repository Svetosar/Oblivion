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

export const can = (me: Me, perm: string) => !!(me.perms && (me.perms["*"] || me.perms[perm]));
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));
export const fmtDate = (ts: number | null) => (ts ? new Date(ts * 1000).toLocaleString() : "—");
