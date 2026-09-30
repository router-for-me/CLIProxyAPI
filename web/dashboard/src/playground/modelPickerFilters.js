// Pure helpers for the ModelPicker component, kept in a .js file so
// node:test can import them without a JSX transform.

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
