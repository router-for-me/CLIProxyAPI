// providerKeyIsLive decides whether a configured upstream picker row should
// render as "live" given the live providers returned by the runtime registry.
//
// PG-backed built-in API-key rows use compound routing keys such as
// "claude:42". The registry emits that same compound key when that specific
// auth row serves the model. A bare key such as "claude" represents a bare
// legacy/OAuth auth and is not evidence that every compound Claude row is
// serving the model; matching it here would mark an unrelated row live.
//
// OpenAI Compatibility entries are matched exactly: an entry-level route
// (e.g. "openai-compatible-foo:bar") is live only when the runtime reports
// that exact entry key. The provider-level route (e.g. "openai-compatible-foo")
// stays live when the runtime reports the provider key OR any of its entry
// keys, so operators pinned to the provider before this change still see it.
//
// Returns false when either argument is missing or unrecognised.
export function providerKeyIsLive(providerKey, liveProviders) {
  const key = typeof providerKey === 'string' ? providerKey.trim().toLowerCase() : '';
  if (!key || !Array.isArray(liveProviders)) return false;
  const liveKeys = new Set(
    liveProviders
      .map((p) => (typeof p === 'string' ? p.trim().toLowerCase() : ''))
      .filter(Boolean),
  );
  if (liveKeys.has(key)) return true;
  // Provider-level OpenAI Compatibility routes also light up when a more
  // specific entry key (e.g. "openai-compatible-foo:bar") is currently live,
  // because the provider's complete pool is implicitly live if any entry in
  // it is serving the model.
  const openaiPrefix = 'openai-compatible-';
  if (key.startsWith(openaiPrefix) && !key.includes(':')) {
    const base = key;
    for (const live of liveKeys) {
      if (live === base) return true;
      if (live.startsWith(`${base}:`)) return true;
    }
    return false;
  }
  return false;
}

// generateEntryIdentity produces the identity string the runtime uses for an
// OpenAI Compatibility API-key entry. Named entries normalise to their lower-
// cased slug; unnamed persisted entries resolve to "key-<id>"; unnamed entries
// without a persisted ID receive an empty string.
//
// Explicit "key-<digits>" names are reserved by the backend as a collision-
// shield against the generated fallback; the dashboard treats them as a
// blank name so the displayed identity still uses the row id rather than a
// value the operator can never actually save.
export function generateEntryIdentity(entry) {
  if (!entry) return '';
  const raw = typeof entry.name === 'string' ? entry.name.trim().toLowerCase() : '';
  if (raw && /^[a-z0-9][a-z0-9_-]*$/.test(raw) && !/^key-[0-9]+$/.test(raw)) {
    return raw;
  }
  const id = Number(entry.id) || 0;
  return id > 0 ? `key-${id}` : '';
}

// expandProviderToChoices turns one upstream row into the route choices that
// should appear in the picker.
//
// Returns an array of objects with the shape:
//   { key, label, providerType, level, identity, count }
//
//   level = 'provider' (the pool route) or 'entry' (a single entry pin).
//
// Empty for disabled rows or rows missing a provider_key. OpenAI Compatibility
// rows always include a provider-level choice and one entry-level choice per
// persisted entry with a derivable identity. API-key secrets are never
// inspected.
export function expandProviderToChoices(row) {
  if (!row || row.disabled) return [];
  const providerKey = typeof row.provider_key === 'string' ? row.provider_key.trim() : '';
  if (!providerKey) return [];

  const providerType = row.provider_type || '';
  const providerLabel = (() => {
    const candidates = [row.name, row.label, row.email, row.file_name];
    for (const c of candidates) {
      const s = typeof c === 'string' ? c.trim() : '';
      if (s) return s;
    }
    return providerKey;
  })();

  // Non-OpenAI providers contribute exactly one provider-level choice.
  if (providerType !== 'openai-compatibility') {
    return [{
      key: providerKey,
      label: providerLabel,
      providerType,
      level: 'provider',
      identity: '',
      count: 1,
    }];
  }

  const entries = Array.isArray(row.api_key_entries) ? row.api_key_entries : [];
  const choices = [{
    key: providerKey,
    label: providerLabel,
    providerType,
    level: 'provider',
    identity: '',
    count: Math.max(1, entries.length),
  }];

  for (const entry of entries) {
    const identity = generateEntryIdentity(entry);
    if (!identity) continue;
    const entryLabel = identity || providerLabel;
    choices.push({
      key: `${providerKey}:${identity}`,
      label: `${providerLabel} · ${entryLabel}`,
      providerType,
      level: 'entry',
      identity,
      count: 1,
    });
  }
  return choices;
}

// expandAllProvidersToChoices walks every configured upstream and produces
// the flat list of picker choices, deduped by routing key. The first occurrence
// wins on display label; the count is summed for colliding keys (legacy rows
// that happen to share a routing key with a built-in row).
export function expandAllProvidersToChoices(rows) {
  if (!Array.isArray(rows)) return [];
  const byKey = new Map();
  for (const row of rows) {
    for (const choice of expandProviderToChoices(row)) {
      const existing = byKey.get(choice.key);
      if (existing) {
        existing.count += 1;
      } else {
        byKey.set(choice.key, { ...choice });
      }
    }
  }
  return [...byKey.values()].sort((a, b) => a.key.localeCompare(b.key));
}

// mergeLiveWithChoices merges live routing keys into a configured-choices list
// while preserving configured-only keys. Live keys that are not in the
// configured list are appended so operators still see a de-registered provider
// that is currently serving the model. Live-key display falls back to the bare
// routing key when no configured upstream row matches.
export function mergeLiveWithChoices(liveProviders, configuredChoices, upstreamByKey) {
  const seen = new Set();
  const out = [];
  for (const choice of configuredChoices) {
    if (!choice || !choice.key || seen.has(choice.key)) continue;
    seen.add(choice.key);
    out.push(choice);
  }
  for (const p of liveProviders || []) {
    const key = String(p || '').trim();
    if (!key || seen.has(key)) continue;
    const upstream = upstreamByKey && upstreamByKey.get(key);
    seen.add(key);
    out.push({
      key,
      label: upstream ? upstream.label : key,
      providerType: upstream ? upstream.providerType : '',
      level: 'provider',
      identity: '',
      count: upstream ? upstream.count : 0,
    });
  }
  return out;
}
