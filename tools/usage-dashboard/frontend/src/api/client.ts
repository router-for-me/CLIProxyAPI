import type { paths } from "./types";
import { buildQuery } from "./queryString";

type GetParams<P extends keyof paths> = paths[P] extends {
  get: { parameters: { query: any } };
}
  ? paths[P]["get"]["parameters"]["query"]
  : Record<string, string | string[] | number | undefined>;

type GetResponse<P extends keyof paths> =
  paths[P] extends { get: { responses: { 200: { content: { "application/json": infer T } } } } }
    ? T
    : never;

export async function apiGet<P extends keyof paths & string>(
  path: P,
  params?: GetParams<P>,
  token?: string,
): Promise<GetResponse<P>> {
  const url = new URL(path, window.location.origin);
  if (params) url.search = buildQuery(params as Record<string, string | string[] | undefined | number>).toString();
  const headers: Record<string, string> = {};
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(url, { headers });
  if (!resp.ok) {
    let detail = "request failed";
    try {
      detail = (await resp.json()).detail ?? detail;
    } catch {
      /* ignore parse errors */
    }
    throw new Error(`${resp.status}: ${detail}`);
  }
  return resp.json() as Promise<GetResponse<P>>;
}

export async function apiPut<P extends keyof paths & string>(
  path: P,
  body: unknown,
  token?: string,
): Promise<unknown> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(path, {
    method: "PUT",
    headers,
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`${resp.status}`);
  return resp.json();
}

export async function apiDelete<P extends keyof paths & string>(
  path: P,
  token?: string,
): Promise<unknown> {
  const headers: Record<string, string> = {};
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(path, { method: "DELETE", headers });
  if (!resp.ok) throw new Error(`${resp.status}`);
  return resp.json();
}