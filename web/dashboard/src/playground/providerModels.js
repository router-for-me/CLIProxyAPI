// Pure helpers for the playground model picker. Kept in a .js file so
// node:test can import them without a JSX transform.
//
// The picker lists models per upstream provider so an operator can see what
// each provider exposes and test with the exact id a client would send.
// The client-facing id is the model's `alias` when set, otherwise its `name`
// (mirrors buildConfiguredModelInfo in sdk/cliproxy/service_models.go).

function str(value) {
  return typeof value === 'string' ? value.trim() : '';
}

// sendIdFor resolves the id a client sends for a provider model row.
export function sendIdFor(model) {
  const alias = str(model?.alias);
  const name = str(model?.name);
  return alias || name;
}

// statusFor merges the live-status row (keyed by provider id) with the
// cooldown provider-key set. Both sources are optional and degrade to
// "unknown" / not-cooling.
function statusFor(provider, cooldownProviders, liveStatus) {
  const idKey = provider?.id != null ? String(provider.id) : '';
  const live = !!(idKey && liveStatus && liveStatus[idKey] && liveStatus[idKey].is_live);
  const providerKey = str(provider?.provider_key).toLowerCase();
  const type = str(provider?.provider_type).toLowerCase();
  const title = str(provider?.name || provider?.label).toLowerCase();
  const cooling = !!(cooldownProviders
    && ((providerKey && cooldownProviders.has(providerKey)) || (type && cooldownProviders.has(type))));
  return { live, cooldown: cooling, providerKey, title };
}

// buildProviderGroups turns the upstream-providers response into one group
// per provider, each holding that provider's testable models. Providers with
// no models are skipped (nothing to test). Rows are deduped by send id within
// a provider; the same id may legitimately appear under several providers.
export function buildProviderGroups(providers, { cooldownProviders = null, liveStatus = null } = {}) {
  if (!Array.isArray(providers)) return [];
  const groups = [];
  for (const p of providers) {
    if (!p) continue;
    const models = Array.isArray(p.models) ? p.models : [];
    const rows = [];
    const seen = new Set();
    for (const m of models) {
      const sendId = sendIdFor(m);
      if (!sendId) continue;
      const key = sendId.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      const alias = str(m.alias);
      const name = str(m.name);
      rows.push({
        sendId,
        name,
        alias,
        displayName: str(m.display_name) || str(m.displayName),
        isAlias: !!alias && alias !== name,
      });
    }
    if (rows.length === 0) continue;
    const status = statusFor(p, cooldownProviders, liveStatus);
    const id = p.id != null ? String(p.id) : status.providerKey || status.title || str(p.provider_type);
    groups.push({
      id,
      title: str(p.name) || str(p.label) || str(p.provider_type) || 'Provider',
      type: str(p.provider_type),
      providerKey: status.providerKey,
      live: status.live,
      cooldown: status.cooldown,
      catalog: false,
      models: rows,
    });
  }
  return groups.sort((a, b) =>
    a.title.localeCompare(b.title) || a.id.localeCompare(b.id),
  );
}

// buildCatalogGroups keeps catalog-only models discoverable, grouped by their
// provider, skipping any id already surfaced by an upstream provider.
export function buildCatalogGroups(catalog, seenIds) {
  if (!Array.isArray(catalog)) return [];
  const seen = new Set(Array.from(seenIds || [], (id) => String(id).toLowerCase()));
  const byProvider = new Map();
  for (const m of catalog) {
    const id = str(m?.model) || str(m?.id);
    if (!id) continue;
    const key = id.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    const provider = str(m.provider) || str(m.provider_type) || 'other';
    if (!byProvider.has(provider)) byProvider.set(provider, []);
    byProvider.get(provider).push({ sendId: id, name: id, alias: '', displayName: '', isAlias: false });
  }
  return Array.from(byProvider.entries())
    .map(([provider, rows]) => ({
      id: `catalog:${provider}`,
      title: provider,
      type: 'catalog',
      providerKey: '',
      live: false,
      cooldown: false,
      catalog: true,
      models: rows,
    }))
    .sort((a, b) => a.title.localeCompare(b.title));
}

// collectSendIds is the set of ids already covered by the upstream groups.
export function collectSendIds(groups) {
  const out = new Set();
  for (const g of groups || []) {
    for (const m of g.models || []) out.add(m.sendId.toLowerCase());
  }
  return out;
}

// filterGroups applies the search query across provider title/type and each
// model's send id, name, and alias.
export function filterGroups(groups, query) {
  const q = str(query).toLowerCase();
  if (!q) return groups;
  const out = [];
  for (const g of groups || []) {
    if (g.title.toLowerCase().includes(q) || (g.type || '').toLowerCase().includes(q)) {
      out.push(g);
      continue;
    }
    const models = (g.models || []).filter((m) =>
      m.sendId.toLowerCase().includes(q)
      || (m.name || '').toLowerCase().includes(q)
      || (m.alias || '').toLowerCase().includes(q),
    );
    if (models.length) out.push({ ...g, models });
  }
  return out;
}
