// pinProvider helpers — pure functions extracted from Picker.jsx so they
// can be unit-tested without React. The Picker calls these from inside
// useCallback closures; the React component is responsible for the
// state updates but the merge logic lives here.

/**
 * mergePinnedAfterPin returns the next Pinned list after a successful
 * pinProvider call. If the provider_key already existed, its priority
 * is updated (and is_live reflects the candidate we just pinned). If
 * not, the new pin is appended with the assigned priority.
 *
 * Pure function — no side effects, no API calls.
 */
export function mergePinnedAfterPin(prevPinned, result, candidate) {
  const pinned = Array.isArray(prevPinned) ? prevPinned : [];
  const exists = pinned.find((p) => p.provider_key === result.provider_key);
  if (exists) {
    return pinned.map((p) =>
      p.provider_key === result.provider_key
        ? { ...p, priority: result.priority, is_live: !!candidate.live }
        : p,
    );
  }
  return [
    ...pinned,
    {
      provider_key: result.provider_key,
      name: candidate.name,
      priority: result.priority,
      is_live: !!candidate.live,
    },
  ];
}

/**
 * pendingKeySet returns a new pending-key map with the given key set to
 * false (removed). Used to clear the spinner state when a pin completes.
 */
export function clearPending(pending, key) {
  if (!pending || !pending[key]) return pending;
  const next = { ...pending };
  delete next[key];
  return next;
}

/**
 * partitionPicker sorts the response into the four quadrants. The
 * server already returns live + stale + pinned; this is mostly a guard
 * against missing arrays so consumers can always iterate.
 */
export function partitionPicker(picker) {
  if (!picker) {
    return { live: [], stale: [], pinned: [] };
  }
  return {
    live: Array.isArray(picker.live) ? picker.live : [],
    stale: Array.isArray(picker.stale) ? picker.stale : [],
    pinned: Array.isArray(picker.pinned) ? picker.pinned : [],
  };
}