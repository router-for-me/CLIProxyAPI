// Management API client. The management key lives only in this module's
// memory: never in localStorage, sessionStorage, cookies, or the URL.

export const PLUGIN_ID = "prox";
export const API_BASE = "/prox/api";

let managementKey = "";
const listeners = new Set<() => void>();

export function setKey(key: string) {
  managementKey = key;
  listeners.forEach((l) => l());
}

export function hasKey() {
  return managementKey !== "";
}

export function onKeyChange(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public body?: unknown,
  ) {
    super(message);
  }
}

export async function request<T>(path: string, init: { method?: string; body?: unknown; absolute?: boolean } = {}): Promise<T> {
  const url = init.absolute ? path : API_BASE + path;
  const headers: Record<string, string> = { Authorization: `Bearer ${managementKey}` };
  if (init.body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(url, {
      method: init.method ?? "GET",
      headers,
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
    });
  } catch {
    throw new ApiError(0, "network", "CLIProxyAPI is unreachable.");
  }
  const text = await res.text();
  let body: unknown = undefined;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    body = undefined;
  }
  if (!res.ok) {
    if (res.status === 401 || res.status === 403) {
      // A rejected key is dropped so the sign-in screen returns.
      managementKey = "";
      listeners.forEach((l) => l());
    }
    const b = body as { error?: string; message?: string } | undefined;
    throw new ApiError(res.status, b?.error ?? `http_${res.status}`, b?.message ?? messageFor(res.status), body);
  }
  return body as T;
}

function messageFor(status: number) {
  switch (status) {
    case 401:
      return "The management key was rejected.";
    case 403:
      return "Remote management is disabled for this address.";
    case 404:
      return "The plugin is not loaded or not enabled.";
    case 429:
      return "Too many requests. Try again shortly.";
    default:
      return `Request failed (${status}).`;
  }
}
