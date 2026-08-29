// Dedicated "playground" API key bootstrap.
//
// The playground must use a real proxy API key (not the management
// password) so traffic is attributable in usage stats and goes through
// the proxy's auth/quota/policy stack. This hook ensures such a key
// exists, named `playground`, and caches its secret in localStorage.
//
// Every API key in NixLLM belongs to an Internal User, so the bootstrap
// also ensures a "playground" Internal User exists before creating the
// key under it.
//
// The first half (resolvePlaygroundKey) is a pure async function
// suitable for direct testing without React. The hook wraps it with
// useEffect/useState for component use.

import { useCallback, useEffect, useState } from 'react';
import { listAPIKeys, createAPIKey, listInternalUsers, createInternalUser } from '../api/client.js';

const STORAGE_KEY = 'nixllm.playground.key';
const PLAYGROUND_USER_ALIAS = 'playground';
export const PLAYGROUND_KEY_NAME = 'playground';

// findOrCreateUser is a small helper extracted for testability. It
// looks up an Internal User by alias, creating one on a 404.
export async function findOrCreateUser({ list, create, alias }) {
  const res = await list({ search: alias, pageSize: 5 });
  const rows = res?.users || res?.items || res || [];
  const hit = rows.find((u) => (u.user_alias || u.alias) === alias);
  if (hit?.id) return hit.id;
  try {
    const created = await create({ user_alias: alias, auto_create_key: false });
    return created?.id || created?.user?.id;
  } catch (e) {
    if (e?.status !== 409) throw e;
    const res2 = await list({ search: alias, pageSize: 5 });
    const rows2 = res2?.users || res2?.items || res2 || [];
    const hit2 = rows2.find((u) => (u.user_alias || u.alias) === alias);
    if (!hit2?.id) throw new Error(`Internal user "${alias}" not found after 409`);
    return hit2.id;
  }
}

export async function resolvePlaygroundKey({ cached, listKeys, createKey, findOrCreateUser: fcu }) {
  if (cached) {
    try {
      const parsed = JSON.parse(cached);
      if (parsed?.secret) return parsed.secret;
    } catch {
      /* fall through to bootstrap */
    }
  }
  const userId = await fcu(PLAYGROUND_USER_ALIAS);
  const existing = await listKeys({ user_id: userId });
  const found = extractKeyRow(existing);
  if (found?.secret || found?.api_key) {
    return found.secret || found.api_key;
  }
  try {
    const created = await createKey(PLAYGROUND_KEY_NAME, userId);
    if (created?.secret || created?.api_key) return created.secret || created.api_key;
    throw new Error('Create returned no secret');
  } catch (e) {
    if (e?.status !== 409) throw e;
    // 409 race: re-list and pick the row.
    const again = await listKeys({ user_id: userId });
    const row = extractKeyRow(again);
    if (!row?.secret && !row?.api_key) {
      throw new Error('Playground key not found after create-409');
    }
    return row.secret || row.api_key;
  }
}

// extractKeyRow finds the playground row in a list response, tolerating
// either a bare array or the wrapped { items | keys } shape returned by
// the API. Avoids the classic array-protocol collision where
// `arr?.keys` short-circuits to Array.prototype.keys.
function extractKeyRow(payload) {
  const rows = Array.isArray(payload)
    ? payload
    : (payload && (payload.items || payload.keys)) || [];
  return rows.find((k) => (k?.name || k?.alias) === PLAYGROUND_KEY_NAME);
}

export function usePlaygroundKey() {
  const [key, setKey] = useState(() => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      const parsed = raw ? JSON.parse(raw) : null;
      return parsed?.secret || null;
    } catch {
      return null;
    }
  });
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(!key);
  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const cached = (() => {
        try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
      })();
      const secret = await resolvePlaygroundKey({
        cached,
        listKeys: async (extra) => {
          const res = await listAPIKeys({ search: PLAYGROUND_KEY_NAME, pageSize: 100, ...(extra || {}) });
          return res;
        },
        createKey: async (name, user_id) => {
          const res = await createAPIKey({ name, user_id });
          return res;
        },
        findOrCreateUser: (alias) => findOrCreateUser({
          list: async (params) => listInternalUsers({ pageSize: 5, search: alias, ...(params || {}) }),
          create: async (payload) => createInternalUser(payload),
          alias,
        }),
      });
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ id: 'playground', secret }));
      setKey(secret);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { if (!key) refresh(); }, [key, refresh]);
  return { key, error, loading, refresh };
}
