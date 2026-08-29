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
  // Defensive: only iterate when healthRows is a real array. The unwrap
  // helper should guarantee this, but a non-iterable here (e.g. an API
  // error object) would otherwise throw "is not iterable" and crash the
  // whole page on first render.
  if (Array.isArray(healthRows)) {
    for (const h of healthRows) {
      if (modelHealthIsLive(h)) byModel.set(h.model, true);
    }
  }
  return Array.isArray(upstreams) ? upstreams.filter((u) => byModel.get(u.model)) : [];
}

// unwrapList extracts the list payload from various API response shapes:
//
//   - bare array                 : [ ... ]
//   - /models-catalog            : { models: [ ... ], page, ... }
//   - /upstream-providers        : { providers: [ ... ] }
//   - /model-health              : { snapshots: [ ... ], settings, ... }
//   - legacy/other endpoints     : { items: [ ... ] }
//
// When the payload is none of the above (an error envelope, a primitive,
// an unknown wrapper), it returns an empty array rather than the raw
// value, so callers can always treat the result as Array.
export function unwrapList(payload) {
  if (Array.isArray(payload)) return payload;
  if (!payload || typeof payload !== 'object') return [];
  for (const key of ['models', 'providers', 'snapshots', 'items', 'keys', 'users']) {
    if (Array.isArray(payload[key])) return payload[key];
  }
  return [];
}
