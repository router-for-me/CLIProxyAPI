// providerKeyIsLive decides whether a configured upstream picker row should
// render as "live" given the live providers returned by the runtime registry.
//
// PG-backed built-in API-key rows use compound routing keys such as
// "claude:42". The registry emits that same compound key when that specific
// auth row serves the model. A bare key such as "claude" represents a bare
// legacy/OAuth auth and is not evidence that every compound Claude row is
// serving the model; matching it here would mark an unrelated row live.
//
// Returns false when either argument is missing or unrecognised.
export function providerKeyIsLive(providerKey, liveProviders) {
  const key = typeof providerKey === 'string' ? providerKey.trim().toLowerCase() : '';
  if (!key || !Array.isArray(liveProviders)) return false;
  return liveProviders.some((liveProvider) => {
    const liveKey = typeof liveProvider === 'string' ? liveProvider.trim().toLowerCase() : '';
    return liveKey !== '' && liveKey === key;
  });
}
