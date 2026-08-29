// Pure helpers for the ModelPicker component, kept in a .js file so
// node:test can import them without a JSX transform.

export function modelHealthIsLive(h) {
  if (!h) return false;
  if (h.status === 'down') return false;
  if ((h.consecutive_failures || 0) >= 3) return false;
  return true;
}

export function filterLiveModels(upstreams, healthRows) {
  const byModel = new Map();
  for (const h of healthRows || []) {
    if (modelHealthIsLive(h)) byModel.set(h.model, true);
  }
  return (upstreams || []).filter((u) => byModel.get(u.model));
}

// Tolerates either a bare array or the wrapped { items | providers | models }
// shape returned by various API endpoints.
export function unwrapList(payload) {
  if (Array.isArray(payload)) return payload;
  return payload?.items || payload?.providers || payload?.models || payload || [];
}
