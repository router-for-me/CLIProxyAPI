// ============================================================================
// Upstream provider editor — form layer
// ============================================================================

// The pure form/data functions for the upstream provider editor: entry
// validation, form hydration, payload building, and schema-driven save
// validation. Extracted from UpstreamProvidersPage.jsx (provider-editor-page
// plan, Task 2) as the single source of truth for the editor page's
// (./index.jsx) form contract.

import {
  isOAuth,
  isOpenAI,
  isOpenCodeGo,
  isClaude,
  MAX_ENTRY_WEIGHT,
} from './schemas.js';

// validateAPIKeyEntries enforces the same rules the backend's
// normalizeUpstreamProviderEntryName applies, plus the duplicate-name check
// across the provider's entries and the per-row weight and priority bounds.
// Returns a map keyed by entry index, each value a { name?, weight?,
// priority? } error bag for that row.
export function validateAPIKeyEntries(entries) {
  const out = {};
  const list = Array.isArray(entries) ? entries : [];
  const seenNames = new Map(); // normalised name → first occurrence idx
  for (let i = 0; i < list.length; i += 1) {
    const e = list[i] || {};
    const errs = {};
    const raw = typeof e.name === 'string' ? e.name : '';
    const trimmed = raw.trim();
    const normalised = trimmed.toLowerCase();
    if (trimmed !== '') {
      if (!/^[a-z0-9][a-z0-9_-]*$/i.test(trimmed)) {
        errs.name = 'Identity may only contain letters, digits, "_" and "-". It must start with a letter or digit.';
      } else if (/^key-[0-9]+$/.test(normalised)) {
        errs.name = `"${trimmed}" is reserved for auto-generated identities. Use a different value or leave blank.`;
      } else if (seenNames.has(normalised)) {
        const firstIdx = seenNames.get(normalised);
        errs.name = firstIdx === i
          ? 'Duplicate identity detected.'
          : `Duplicate identity. Entry ${firstIdx + 1} already uses "${trimmed}".`;
      }
    }
    // Weight: blank means default. Anything else must be a positive
    // integer in 1..MAX_ENTRY_WEIGHT (inclusive). The backend re-bounds
    // out-of-range values, but the editor keeps the user out of bad
    // state up front.
    const weightRaw = e && e.weight;
    if (weightRaw !== undefined && weightRaw !== null && String(weightRaw).trim() !== '') {
      const s = String(weightRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || !/^-?\d+$/.test(s) || n < 1 || n > MAX_ENTRY_WEIGHT || Math.floor(n) !== n) {
        errs.weight = `Weight must be a whole number between 1 and ${MAX_ENTRY_WEIGHT}.`;
      }
    }
    // Priority: blank means inherit the row-level priority. Unlike weight,
    // any integer is legal — including 0 (an explicit tier) and negatives
    // (below default) — matching the Go *int semantics.
    const prioRaw = e && e.priority;
    if (prioRaw !== undefined && prioRaw !== null && String(prioRaw).trim() !== '') {
      const s = String(prioRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || !/^-?\d+$/.test(s) || Math.floor(n) !== n) {
        errs.priority = 'Priority must be a whole number (blank = inherit the row priority).';
      }
    }
    // MaxConcurrent / MaxWaitMs: blank = unlimited/default; anything else must
    // be a non-negative whole number (0 is explicit "off" for max_concurrent,
    // and the default wait for max_wait_ms).
    const mcRaw = e && e.max_concurrent;
    if (mcRaw !== undefined && mcRaw !== null && String(mcRaw).trim() !== '') {
      const s = String(mcRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || !/^\d+$/.test(s) || Math.floor(n) !== n) {
        errs.max_concurrent = 'Max concurrent must be a non-negative whole number (blank / 0 = unlimited).';
      }
    }
    const mwRaw = e && e.max_wait_ms;
    if (mwRaw !== undefined && mwRaw !== null && String(mwRaw).trim() !== '') {
      const s = String(mwRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || !/^\d+$/.test(s) || Math.floor(n) !== n) {
        errs.max_wait_ms = 'Max wait must be a non-negative whole number of milliseconds (blank = default).';
      }
    }
    // BudgetUSD: blank = no budget. Anything else must be a finite,
    // non-negative amount in USD (decimal values allowed).
    const budgetRaw = e && e.budget_usd;
    if (budgetRaw !== undefined && budgetRaw !== null && String(budgetRaw).trim() !== '') {
      const s = String(budgetRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || n < 0) {
        errs.budget_usd = 'Budget must be a non-negative number in USD (blank = no budget).';
      }
    }
    if (Object.keys(errs).length > 0) out[i] = errs;
    if (trimmed !== '' && !errs.name) {
      seenNames.set(normalised, i);
    }
  }
  return out;
}

// idHintForIdentity renders the row's eventual route identity for the hint
// line. Existing rows use the server-returned id; fresh rows omit the hint.
// Exported because the list page's entries editor still renders these hints.
export function idHintForIdentity(e) {
  const id = Number(e && e.id) || 0;
  const normalised = typeof e.name === 'string' ? e.name.trim().toLowerCase() : '';
  const valid = normalised && /^[a-z0-9][a-z0-9_-]*$/.test(normalised) && !/^key-[0-9]+$/.test(normalised);
  if (valid) return `Route identity: ${normalised}.`;
  if (id > 0) return `Route identity: key-${id}.`;
  return 'Blank identity will become key-<id> after save.';
}

// ============================================================================
// Form construction & payload building
// ============================================================================

function hydrateEntries(src) {
  // Shared row-shape used by both OpenAI Compatibility and Claude (API Key).
  // Round-trip the persisted child-row id, the server-normalised name, the
  // optional per-entry proxy override, the optional weight, and the optional
  // selection tier (priority) so subsequent saves update rows in place
  // rather than deleting and reinserting them.
  if (!Array.isArray(src)) return [];
  return src.map((e) => {
    const poolId = e && e.proxy_pool_id !== undefined && e.proxy_pool_id !== null ? e.proxy_pool_id : '';
    const manualURL = e && e.proxy_url ? e.proxy_url : '';
    return {
    id: Number(e && e.id) || 0,
    name: e && e.name ? e.name : '',
    api_key: e && e.api_key ? e.api_key : '',
    // Pool binding and the manual proxy URL are mutually exclusive; a
    // persisted pool binding wins on hydrate and clears the stale URL.
    proxy_pool_id: poolId !== '' ? poolId : '',
    proxy_url: poolId !== '' ? '' : manualURL,
    // Weight is optional; editors leave it blank for the "default" affordance.
    // Treat null/undefined as blank so the editor shows an empty input.
    weight: e && e.weight !== undefined && e.weight !== null ? e.weight : '',
    // Priority (selection tier) is optional too; null/undefined inherits the
    // row-level priority and renders as a blank input. Unlike weight, an
    // explicit 0 is meaningful (tier 0) and must survive the round-trip.
    priority: e && e.priority !== undefined && e.priority !== null ? e.priority : '',
    // Disabled (per-entry on/off toggle) hydrates to a plain boolean so the
    // editor switch always has a defined value.
    disabled: !!(e && e.disabled),
    // MaxConcurrent / MaxWaitMs are optional per-entry in-flight caps.
    // null/0 = unlimited (feature off); blank input = unset. The editor keeps
    // them as blank strings so a cleared field round-trips to "unset".
    max_concurrent: e && e.max_concurrent !== undefined && e.max_concurrent !== null ? e.max_concurrent : '',
    max_wait_ms: e && e.max_wait_ms !== undefined && e.max_wait_ms !== null ? e.max_wait_ms : '',
    // BudgetUSD is the optional per-entry USD budget cap surfaced by the
    // Provider Budget page. null/undefined = no budget → blank input.
    budget_usd: e && e.budget_usd !== undefined && e.budget_usd !== null ? e.budget_usd : '',
    // Auto-disabled runtime flags are read-only in the editor (written by the
    // server-side sink). Hydrated so the badge + Re-enable action can render.
    auto_disabled: !!(e && e.auto_disabled),
    auto_disabled_at: e && e.auto_disabled_at ? e.auto_disabled_at : '',
    auto_disabled_reason: e && e.auto_disabled_reason ? e.auto_disabled_reason : '',
    };
  });
}

// buildForm hydrates a provider row (or nothing, for create) into the
// editor's flat form shape. Called by the routed editor page (./index.jsx)
// on mount and on provider-type switches (carryOver preserves the
// operator's in-progress values across the switch); also exercised
// directly by ./editor.test.js.
export function buildForm(providerType, initial, carryOver) {
  const src = initial || {};
  const carry = carryOver || {};
  const base = {
    provider_type: providerType || src.provider_type || '',
    name: carry.name ?? src.name ?? '',
    label: carry.label ?? src.label ?? '',
    api_key: carry.api_key ?? src.api_key ?? '',
    base_url: carry.base_url ?? src.base_url ?? '',
    proxy_url: carry.proxy_url ?? src.proxy_url ?? '',
    proxy_pool_id: carry.proxy_pool_id ?? src.proxy_pool_id ?? '',   // row-level pool binding (renderer-resolved)
    relay_base_url: src.relay_base_url ?? '',                        // renderer-managed; display only
    prefix: carry.prefix ?? src.prefix ?? '',
    email: src.email ?? '',
    file_name: src.file_name ?? '',
    priority: carry.priority ?? src.priority ?? 0,
    // In-pool routing strategy for entry-bearing providers. Blank keeps the
    // row unset so the global routing.strategy applies; carried across
    // provider-type switches like the row-level priority above.
    routing_strategy: carry.routing_strategy ?? src.routing_strategy ?? '',
    // Pool-level circuit breaker opt-in (design G3). Mirrors `disabled`:
    // hydrated from the row, always emitted as a boolean in buildPayload so
    // any dashboard edit preserves the flag (management Update is a
    // full-row replace — omitting it would silently reset it to false).
    circuit_breaker: !!(src && src.circuit_breaker),
    // Per-provider request/response capture opt-in (Privacy section).
    // Always emitted as a boolean in buildPayload for the same full-row
    // replace reason as circuit_breaker: omitting it would reset it to false.
    store_request_bodies: !!(src && src.store_request_bodies),
    disabled: src.disabled ?? false,
    websockets: src.websockets ?? false,
    rebuild_mid_system_message: src.rebuild_mid_system_message ?? false,
    experimental_cch_signing: src.experimental_cch_signing ?? false,
    disable_cooling: (src.extra_config && src.extra_config.disable_cooling) ?? false,
    // opencode-go quota probe override (extra_config.quota_url).
    quota_url: (src.extra_config && src.extra_config.quota_url) || '',
    // neuralwatt service tier (extra_config.service_tier). Hydrated blank
    // when absent so the operator's selector defaults to "(provider default)"
    // and the field is omitted from the payload on save (the store keeps the
    // provider default when service_tier is missing).
    service_tier: (src.extra_config && src.extra_config.service_tier) || '',
    // Provider-level auto-disable config (Auto-Disable feature). Codes are a
    // string list (empty = feature off); cooldown is a nullable int (blank/0
    // = manual re-enable only). Both are hydrated blank so a cleared field
    // saves back as unset.
    auto_disable_error_codes: Array.isArray(src.auto_disable_error_codes) ? [...src.auto_disable_error_codes] : [],
    auto_disable_cooldown_seconds: src.auto_disable_cooldown_seconds !== undefined && src.auto_disable_cooldown_seconds !== null
      ? src.auto_disable_cooldown_seconds : '',
    headers: [],
    models: [],
    excluded_models: src.excluded_models ?? [],
    api_key_entries: [],
    // Cloak
    cloak_mode: src.cloak_mode ?? '',
    cloak_strict_mode: src.cloak_strict_mode ?? false,
    cloak_sensitive_words: src.cloak_sensitive_words ?? [],
    cloak_cache_user_id: src.cloak_cache_user_id ?? false,
    // OAuth tokens
    token_access_token: src.token_access_token ?? '',
    token_refresh_token: src.token_refresh_token ?? '',
    token_expiry: src.token_expiry ? formatRFC3339(src.token_expiry) : '',
    token_expired: src.token_expired ?? false,
  };

  // Hydrate models (camelCase shape from the API).
  if (Array.isArray(src.models) && src.models.length > 0) {
    base.models = src.models.map((m) => ({
      name: m.name || '',
      alias: m.alias || '',
      'display-name': m.display_name || m.displayName || '',
      'force-mapping': !!m.force_mapping,
      'fork': !!m.fork,
      image: !!m.image,
      'input-modalities': m.input_modalities || m.inputModalities || [],
      'output-modalities': m.output_modalities || m.outputModalities || [],
      'wire-format': m.wire_format || m.wireFormat || '',
    }));
  }

  // Hydrate headers map → KeyValueEditor rows.
  if (src.headers && typeof src.headers === 'object') {
    base.headers = Object.entries(src.headers).map(([key, value]) => ({ key, value: String(value) }));
  }

  // Hydrate OpenAI Compatibility and Claude (API Key) api_key_entries.
  // Claude also accepts a legacy single api_key; when entries are absent
  // but the legacy api_key is set, synthesise one unsaved entry so the
  // existing pg-backed child-row ID upsert replaces the legacy key
  // transparently on save.
  const entries = hydrateEntries(src.api_key_entries);
  if (entries.length > 0) {
    base.api_key_entries = entries;
  } else if (providerType === 'claude-api-key' && typeof src.api_key === 'string' && src.api_key.trim()) {
    base.api_key_entries = [{
      id: 0,
      name: '',
      api_key: src.api_key,
      proxy_url: '',
      weight: '',
      priority: '',
      disabled: false,
      max_concurrent: '',
      max_wait_ms: '',
      budget_usd: '',
      auto_disabled: false,
      auto_disabled_at: '',
      auto_disabled_reason: '',
    }];
  }

  return base;
}

// buildPayload serializes the editor form into the JSON body POSTed/PUT to
// /upstream-providers. Called by the routed editor page (./index.jsx) on
// save; also exercised directly by ./editor.test.js.
export function buildPayload(form, providerType) {
  const oauth = isOAuth(providerType);
  const openai = isOpenAI(providerType);
  const claude = isClaude(providerType);

  const headersMap = {};
  (form.headers || []).forEach(({ key, value }) => {
    const k = (key || '').trim();
    if (k) headersMap[k] = value;
  });

  const cleanedModels = (form.models || [])
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const m = {
        name: (r.name || '').trim(),
        alias: (r.alias || '').trim(),
        display_name: (r['display-name'] || '').trim(),
        force_mapping: !!r['force-mapping'],
        fork: !!r['fork'],
      };
      if (openai) {
        if (r.image) m.image = true;
        const im = r['input-modalities'];
        if (Array.isArray(im) && im.length) m.input_modalities = im;
        const om = r['output-modalities'];
        if (Array.isArray(om) && om.length) m.output_modalities = om;
      }
      // Per-model upstream wire format (opencode-go only; empty = the
      // executor's openai default). Emitted whenever set so an operator can
      // also clear it back to '' explicitly.
      if (isOpenCodeGo(providerType)) {
        const wf = String(r['wire-format'] || '').trim();
        if (wf) m.wire_format = wf;
      }
      return m;
    });

  // Build extra_config from the form's toggle fields + any pre-existing
  // extra_config keys (e.g. request_retry, tool_prefix_disabled populated
  // during the OAuth connect flow).
  const extra = { ...(typeof form.extra_config === 'object' ? form.extra_config : {}) };
  if (form.disable_cooling) extra.disable_cooling = true;
  // opencode-go quota probe override: emitted when set, dropped when cleared.
  if (isOpenCodeGo(providerType)) {
    const quotaURL = (form.quota_url || '').trim();
    if (quotaURL) extra.quota_url = quotaURL; else delete extra.quota_url;
  }
  // neuralwatt service tier: emitted when set, dropped when cleared. Lives in
  // extra_config because the upstream store has no dedicated column for it
  // (the value is rendered back onto config.NeuralwattKey.ServiceTier at
  // render time). Blank keeps the provider default.
  if (providerType === 'neuralwatt-api-key') {
    const tier = (form.service_tier || '').trim();
    if (tier) extra.service_tier = tier; else delete extra.service_tier;
  }
  // Don't send an empty object — extra_config defaults to '{}' server-side.
  const extraConfig = Object.keys(extra).length > 0 ? extra : {};

  const payload = {
    provider_type: providerType,
    priority: Number(form.priority) || 0,
    disabled: !!form.disabled,
    // Always emitted as a boolean so editing any other field keeps the
    // persisted opt-in (a full-row Update must not reset it).
    circuit_breaker: !!form.circuit_breaker,
    // Always emitted as a boolean so editing any other field preserves the
    // persisted request/response capture opt-in (full-row Update).
    store_request_bodies: !!form.store_request_bodies,
    prefix: (form.prefix || '').trim(),
    base_url: (form.base_url || '').trim(),
    proxy_url: (form.proxy_url || '').trim() || 'none',
    headers: headersMap,
    models: cleanedModels,
    excluded_models: form.excluded_models || [],
    extra_config: extraConfig,
  };

  if (oauth) {
    payload.email = (form.email || '').trim();
    payload.file_name = (form.file_name || '').trim();
    payload.label = (form.label || '').trim();
    payload.token_access_token = form.token_access_token || '';
    payload.token_refresh_token = form.token_refresh_token || '';
    payload.token_expired = !!form.token_expired;
    if (form.token_expiry) {
      const t = new Date(form.token_expiry);
      if (!Number.isNaN(t.getTime())) {
        payload.token_expiry = t.toISOString();
      }
    }
    // Provider-level auto-disable config is meaningful for entry-bearing
    // providers (it auto-disables individual api_key_entries). OAuth rows
    // have no entries, so the fields are not emitted for them.
  } else if (openai || claude || isOpenCodeGo(providerType)) {
    // Provider-level auto-disable config for entry-bearing providers.
    // Codes are emitted as a string list (empty = feature off); cooldown is a
    // non-negative int or omitted (blank/0 = manual re-enable only).
    payload.auto_disable_error_codes = Array.isArray(form.auto_disable_error_codes)
      ? [...form.auto_disable_error_codes]
      : [];
    const cdRaw = form.auto_disable_cooldown_seconds;
    if (cdRaw !== undefined && cdRaw !== null && String(cdRaw).trim() !== '') {
      const n = Number(cdRaw);
      if (Number.isInteger(n) && n >= 0) payload.auto_disable_cooldown_seconds = n;
    }
    // Entry-bearing providers: openai-compatibility, claude-api-key, and
    // opencode-go all use the multi-row api_key_entries editor below.
    // Row-level proxy pool binding: emitted only when set so a cleared
    // picker keeps the manual proxy_url (or none) semantics intact.
    const rowPoolId = Number(form.proxy_pool_id);
    if (Number.isFinite(rowPoolId) && rowPoolId > 0) {
      payload.proxy_pool_id = rowPoolId;
    }
    // OpenAI Compatibility AND Claude (API Key) both use the multi-row
    // entries editor in the modern UI; both round-trip the persisted
    // child-row id, the optional normalised identity, the optional
    // per-entry proxy override, and the optional weight for weighted
    // round-robin. New rows omit the id so the backend treats them as
    // inserts. Blank entries (no api_key) are filtered out so a Save
    // never resends rows the operator cleared.
    payload.name = (form.name || '').trim();
    // Routing strategy: only emitted when the operator picked one; empty
    // keeps the row unset so the global routing.strategy applies.
    const strategy = (form.routing_strategy || '').trim();
    if (strategy) payload.routing_strategy = strategy;
    payload.api_key_entries = (Array.isArray(form.api_key_entries) ? form.api_key_entries : [])
      .filter((e) => e && e.api_key && String(e.api_key).trim())
      .map((e) => {
        const entry = {
          api_key: String(e.api_key).trim(),
        };
        // Mutually exclusive: a picked pool clears the manual URL and vice
        // versa. Pool binding emitted as a number; manual URL only when no
        // pool is picked.
        const poolId = Number(e.proxy_pool_id);
        if (Number.isFinite(poolId) && poolId > 0) {
          entry.proxy_pool_id = poolId;
        } else {
          entry.proxy_url = (e.proxy_url || '').trim();
        }
        const id = Number(e.id) || 0;
        if (id > 0) entry.id = id;
        const name = typeof e.name === 'string' ? e.name.trim().toLowerCase() : '';
        if (name) entry.name = name;
        // Weight: only emitted when the operator filled it in with a
        // valid positive integer; blank or malformed values fall back to
        // the scheduler default (1). The validate() pass surfaces
        // invalid weights as inline row errors before Save.
        const weightRaw = e.weight;
        if (weightRaw !== undefined && weightRaw !== null && String(weightRaw).trim() !== '') {
          const s = String(weightRaw).trim();
          const n = Number(s);
          if (Number.isFinite(n) && /^-?\d+$/.test(s) && n >= 1 && n <= MAX_ENTRY_WEIGHT && Math.floor(n) === n) {
            entry.weight = Math.trunc(n);
          }
        }
        // Priority: emitted when filled and a whole number; blank = inherit
        // the row-level priority. Explicit 0 is meaningful (tier 0) and must
        // be sent as 0, not dropped — unlike weight, where 0 is excluded.
        const prioRaw = e.priority;
        if (prioRaw !== undefined && prioRaw !== null && String(prioRaw).trim() !== '') {
          const s = String(prioRaw).trim();
          const n = Number(s);
          if (Number.isFinite(n) && /^-?\d+$/.test(s) && Math.floor(n) === n) {
            entry.priority = Math.trunc(n);
          }
        }
        // Per-entry on/off toggle: always emitted as a boolean. A disabled
        // entry with a key stays in the payload (persisted, but the renderer
        // excludes it from config.yaml); only blank-key entries are filtered.
        entry.disabled = !!e.disabled;
        // Per-entry concurrency cap: emitted when filled as a non-negative
        // whole number; 0 = unlimited (feature off). Blank/empty = unset.
        // Serialized as numbers, never strings, so the backend *int decode
        // accepts them.
        const mcRaw = e.max_concurrent;
        if (mcRaw !== undefined && mcRaw !== null && String(mcRaw).trim() !== '') {
          const n = Number(mcRaw);
          if (Number.isInteger(n) && n >= 0) entry.max_concurrent = n;
        }
        const mwRaw = e.max_wait_ms;
        if (mwRaw !== undefined && mwRaw !== null && String(mwRaw).trim() !== '') {
          const n = Number(mwRaw);
          if (Number.isInteger(n) && n >= 0) entry.max_wait_ms = n;
        }
        // BudgetUSD: optional per-entry USD budget cap for the Provider
        // Budget page. Emitted only when filled with a non-negative number;
        // blank = no budget (the store leaves the column NULL).
        const budgetRaw = e.budget_usd;
        if (budgetRaw !== undefined && budgetRaw !== null && String(budgetRaw).trim() !== '') {
          const n = Number(budgetRaw);
          if (Number.isFinite(n) && n >= 0) entry.budget_usd = n;
        }
        // Auto-disabled runtime flags: a manual Re-enable needs the PUT to
        // carry auto_disabled=false so it survives the backend's COALESCE
        // preserve-on-update. Everything on this row is read-only; only the
        // Re-enable action flips auto_disabled back to false.
        entry.auto_disabled = !!e.auto_disabled;
        if (e.auto_disabled_at) entry.auto_disabled_at = e.auto_disabled_at;
        if (e.auto_disabled_reason) entry.auto_disabled_reason = e.auto_disabled_reason;
        return entry;
      });
  } else {
    // Other API-key providers (gemini, codex, xai, interactions,
    // vertex). The Identifier field maps to the generic `name` column
    // (lower-cased by the proxy into the routing provider key).
    payload.name = (form.name || '').trim();
    payload.api_key = (form.api_key || '').trim();
  }

  if (claude) {
    // The Identifier field is mapped to the generic `name` column.
    payload.name = (form.name || '').trim();
    payload.rebuild_mid_system_message = !!form.rebuild_mid_system_message;
    payload.experimental_cch_signing = !!form.experimental_cch_signing;
    payload.cloak_mode = form.cloak_mode || '';
    payload.cloak_strict_mode = !!form.cloak_strict_mode;
    payload.cloak_sensitive_words = form.cloak_sensitive_words || [];
    payload.cloak_cache_user_id = !!form.cloak_cache_user_id;
  }

  // Codex/xAI websockets.
  if (providerType === 'codex-api-key' || providerType === 'xai-api-key') {
    payload.websockets = !!form.websockets;
  }

  return payload;
}

// ============================================================================
// Validation
// ============================================================================

// validate runs the schema-driven per-field checks plus the entry-pool
// checks (identity rules, weight bounds, credential-required) and returns a
// field-name → message map consumed by the routed editor page (./index.jsx)
// for both the inline errors and the aggregate fix-N banner; also exercised
// directly by ./editor.test.js.
export function validate(form, schema, providerType, siblingNames, isEdit) {
  const errors = {};
  for (const section of schema.sections) {
    for (const field of section.fields) {
      const v = form[field.name];
      if (field.required && !field._skipRequired && (v === '' || v === null || v === undefined ||
          (Array.isArray(v) && v.length === 0 && field.type !== 'chips'))) {
        // For arrays/toggles, empty isn't necessarily invalid. Only flag
        // text/password/number/select required-as-non-empty.
        if (['text', 'password', 'number'].includes(field.type)) {
          if (!v || (typeof v === 'string' && !v.trim())) {
            errors[field.name] = `${field.label} is required.`;
            continue;
          }
        }
      }
      if (field.validate && v) {
        const msg = field.validate(v);
        if (msg) errors[field.name] = msg;
      }
    }
  }
  // OpenAI-compat and Claude (API Key) entry validation: identity
  // uniqueness/reserved-name rules + per-row weight bounds. Mirrors the
  // backend's normalizeUpstreamProviderEntryName + credentialweight
  // parsing so Save cannot submit a payload the server would reject.
  const usesEntries = providerType === 'openai-compatibility' || providerType === 'claude-api-key';
  if (usesEntries) {
    // OpenAI-compat: provider-level name uniqueness.
    if (isOpenAI(providerType)) {
      const name = (form.name || '').trim();
      if (name && siblingNames.includes(name)) {
        // In edit mode, the current row's own name is in siblingNames too;
        // we can't distinguish without the editing row's id in the list, so
        // only flag duplicates when there are 2+ occurrences.
        const occurrences = siblingNames.filter((n) => n === name).length;
        if (!isEdit || occurrences > 1) {
          errors.name = `Another provider already uses the name "${name}".`;
        }
      }
    }
    // Entries are always coerced into an array; tolerate malformed input
    // safely so a never-crash contract holds.
    const rawEntries = Array.isArray(form.api_key_entries) ? form.api_key_entries : [];
    const entryErrors = validateAPIKeyEntries(rawEntries);
    const problemCount = Object.keys(entryErrors).length;
    if (problemCount > 0) {
      errors.api_key_entries = `${problemCount} API key entr${problemCount === 1 ? 'y has' : 'ies have'} an identity or priority problem. See inline messages below.`;
    }
    // Claude (API Key) requires AT LEAST one non-blank entry (which
    // includes legacy rows carrying a single api_key). Without it the
    // operator would save an auth-less provider.
    if (providerType === 'claude-api-key') {
      const hasNonblankEntry = rawEntries.some((e) =>
        e && typeof e.api_key === 'string' && e.api_key.trim() !== '',
      );
      if (!hasNonblankEntry) {
        // Reuse the api_key_entries bucket so the inline editor + the
        // Save banner both surface the same credential-required message.
        if (!errors.api_key_entries) errors.api_key_entries = 'At least one API key is required.';
      }
    }
  }
  return errors;
}

// formatRFC3339 converts an arbitrary timestamp to a strict RFC3339 form
// (no millisecond component) for the token-expiry input. Imported by the
// page (OAuth connect merge) and used by buildForm.
export function formatRFC3339(t) {
  if (!t) return '';
  try {
    const d = new Date(t);
    if (Number.isNaN(d.getTime())) return '';
    return d.toISOString().replace(/\.\d{3}Z$/, 'Z');
  } catch { return ''; }
}
