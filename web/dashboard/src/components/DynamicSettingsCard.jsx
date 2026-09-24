import React, { useState } from 'react';
import {
  getRuntimeConfig,
  updateRuntimeConfig,
  extractRevisionConflict,
} from '../api/client.js';
import { Spinner, ErrorBanner } from './Primitives.jsx';
import RevisionConflictModal from './RevisionConflictModal.jsx';

// DynamicSettingsCard — renders every leaf key under `snapshot.settings` with
// a typed input, grouped into named sections by top-level key prefix.
//
// Replaces the previous RuntimeConfigPage which only hard-coded four fields
// (port, host, tls, remote-management). All other settings in the snapshot
// (redis, quota-share, cooldown-wait, logging, pprof, usage-statistics,
// transient-error-cooldown-seconds, future additions) are now visible and
// editable.
//
// Save model: each field calls `onApply({ [path]: newValue })` on blur (text
// / number) or change (checkbox). The apply handler merges into the active
// snapshot under expected_revision. 409 conflicts surface through the
// RevisionConflictModal passed in as the `conflict` prop.

// --- Pure helpers (exported for tests) ------------------------------------

// SECTION_DEFINITIONS: ordered list of (key, title, predicate) tuples. The
// first predicate that returns true for a top-level key claims it. Order
// matters: more specific prefixes (e.g. "redis-usage-queue-retention-
// seconds") must be tested before generic ones (e.g. anything starting with
// "redis-"). The Advanced section is the implicit fallback when nothing
// matches.
//
// `key` is the stable identifier used as the object's key in the
// groupSettingsBySection return value; `title` is the human label rendered
// in the dashboard.
const SECTION_DEFINITIONS = [
  {
    key: 'ServerBinding',
    title: 'Server binding',
    match: (k) => (
      k === 'host'
      || k === 'port'
      || k === 'tls'
      || k === 'remote-management'
    ),
  },
  {
    key: 'Logging',
    title: 'Logging',
    match: (k) => /^(logging|logs|error-logs)/i.test(k),
  },
  {
    key: 'Usage',
    title: 'Usage',
    match: (k) => /^(redis|usage)/i.test(k),
  },
  {
    key: 'Quota',
    title: 'Quota',
    match: (k) => /^(quota|ws-auth)/i.test(k),
  },
  {
    key: 'Routing',
    title: 'Routing',
    match: (k) => k === 'routing',
  },
  {
    key: 'Cooling',
    title: 'Cooldown & telemetry',
    match: (k) => /^(disable-cooling|cooldown|save-cooldown|transient-error|max-retry|request-retry|auth-auto-refresh|pprof)/i.test(k),
  },
  {
    key: 'Antigravity',
    title: 'Antigravity',
    match: (k) => /^antigravity/i.test(k),
  },
  {
    key: 'Providers',
    title: 'Providers',
    match: (k) => (
      k === 'xai'
      || k === 'meta'
      || k === 'codex'
      || k === 'codex-header-defaults'
      || k === 'claude-header-defaults'
      || k === 'disable-claude-cloak-mode'
    ),
  },
  {
    key: 'OAuth',
    title: 'OAuth',
    match: (k) => /^oauth/i.test(k),
  },
  {
    key: 'Payload',
    title: 'Payload / debug',
    match: (k) => k === 'payload' || k === 'debug',
  },
  {
    key: 'Surface',
    title: 'Surface',
    match: (k) => k === 'commercial-mode' || k === 'branding',
  },
  {
    key: 'Concurrency',
    title: 'Concurrency',
    match: (k) => k === 'credential-concurrency' || k === 'credential-in-flight',
  },
  {
    key: 'Plugins',
    title: 'Plugins',
    match: (k) => k === 'plugins',
  },
];

// groupSettingsBySection walks the top-level keys of `settings` and assigns
// each one to the first matching section. Anything left over lands in
// "Advanced" so a future scalarKey added on the server is still editable
// without a dashboard release.
//
// Returns: { [sectionKey]: string[] } where each entry is the list of
// top-level settings keys that belong to that section. The keys are
// stable identifiers (e.g. "ServerBinding"); the rendered human title is
// looked up via sectionTitle().
export function groupSettingsBySection(settings) {
  const out = {};
  for (const def of SECTION_DEFINITIONS) out[def.key] = [];
  out.Advanced = [];

  if (!settings || typeof settings !== 'object') return out;

  for (const key of Object.keys(settings)) {
    const def = SECTION_DEFINITIONS.find((d) => d.match(key));
    const bucket = def ? def.key : 'Advanced';
    out[bucket].push(key);
  }
  return out;
}

// sectionTitle returns the human-readable title for a section key (e.g.
// "Server binding" for "ServerBinding"); used by the renderer.
export function sectionTitle(sectionKey) {
  const def = SECTION_DEFINITIONS.find((d) => d.key === sectionKey);
  return def ? def.title : (sectionKey === 'Advanced' ? 'Advanced (other)' : sectionKey);
}

// inferInputType chooses an input widget for a leaf value. The type derives
// from the JS value's runtime shape, with two naming-based overrides:
//   - any key whose name ends in `password`, `secret`, `token`, or `key`
//     renders as a password input (even if the value is empty);
//   - nested objects / arrays never get their own widgets — they render as
//     JSON textareas so the operator can edit them in place.
//
// Returns one of: 'bool', 'number', 'password', 'text', 'object', 'array',
// 'json'.
export function inferInputType(value, keyName) {
  if (typeof value === 'boolean') return 'bool';
  if (typeof value === 'number') return 'number';
  if (Array.isArray(value)) return 'array';
  if (value !== null && typeof value === 'object') return 'object';

  const name = String(keyName || '');
  if (/(password|secret|token|key)$/i.test(name)) return 'password';

  // Non-string scalars other than bool/number (e.g. null, bigint) fall back
  // to a JSON textarea so the operator can edit them without losing data.
  if (typeof value !== 'string') return 'json';

  return 'text';
}

// setByPath writes `value` into `obj` at the dot-separated `path`, creating
// intermediate objects as needed. Existing objects at intermediate positions
// are preserved (only the leaf key is overwritten). The path uses '.' as the
// separator exclusively; key names that contain literal dots are not
// supported by this settings shape.
export function setByPath(obj, path, value) {
  if (!obj || typeof path !== 'string') return;
  const parts = path.split('.');
  let cursor = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    const k = parts[i];
    if (cursor[k] === null || typeof cursor[k] !== 'object' || Array.isArray(cursor[k])) {
      cursor[k] = {};
    }
    cursor = cursor[k];
  }
  cursor[parts[parts.length - 1]] = value;
}

// getByPath reads `obj` at the dot-separated `path`. Returns undefined for
// missing intermediate objects or leaves.
export function getByPath(obj, path) {
  if (!obj || typeof path !== 'string') return undefined;
  const parts = path.split('.');
  let cursor = obj;
  for (const k of parts) {
    if (cursor === null || typeof cursor !== 'object') return undefined;
    if (!(k in cursor)) return undefined;
    cursor = cursor[k];
  }
  return cursor;
}

// --- Component -------------------------------------------------------------

export default function DynamicSettingsCard() {
  const [snapshot, setSnapshot] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const [conflict, setConflict] = useState(null);

  const reload = async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const resp = await getRuntimeConfig();
      setSnapshot(resp?.snapshot ?? null);
    } catch (err) {
      setLoadError(err);
    } finally {
      setLoading(false);
    }
  };

  // Load on mount.
  React.useEffect(() => { void reload(); }, []);

  const handleApply = async (settingsPatch, extraPatch, { forceRevision } = {}) => {
    if (!snapshot) return;
    setSaving(true);
    setSaveError(null);
    setConflict(null);
    const expected = forceRevision ?? snapshot.revision;
    const merged = { ...snapshot.settings, ...settingsPatch };
    const mergedExtra = { ...(snapshot.extra || {}) };
    if (extraPatch) Object.assign(mergedExtra, extraPatch);
    try {
      const resp = await updateRuntimeConfig({
        expectedRevision: expected,
        settings: merged,
        extra: mergedExtra,
      });
      setSnapshot(resp.snapshot ?? snapshot);
    } catch (err) {
      const c = extractRevisionConflict(err);
      if (c) setConflict(c);
      else setSaveError(err);
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = async () => {
    setConflict(null);
    await reload();
  };

  const handleForceSave = async () => {
    if (!conflict) return;
    await handleApply({}, null, { forceRevision: conflict.activeRevision });
  };

  if (loading) return <Spinner label="Loading runtime config…" />;
  if (loadError) return <ErrorBanner error={loadError} onRetry={reload} />;
  if (!snapshot) return <ErrorBanner error={new Error('No snapshot returned')} />;

  const settings = snapshot.settings || {};
  const grouped = groupSettingsBySection(settings);

  return (
    <>
      <div className="row row--between" style={{ alignItems: 'baseline' }}>
        <div>
          <h3 className="card__title" style={{ margin: 0 }}>Runtime config</h3>
          <p className="muted" style={{ marginTop: 4, marginBottom: 0 }}>
            All settings persisted in the active <code>runtime_config</code>{' '}
            snapshot. Each field saves independently on blur (or on change
            for checkboxes).
          </p>
        </div>
        <div className="muted">revision {snapshot.revision} · {snapshot.updated_source || 'system'}</div>
      </div>

      {saveError && <ErrorBanner error={saveError} onRetry={() => setSaveError(null)} />}
      {saving && <Spinner label="Saving…" />}

      <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
        {Object.entries(grouped).map(([sectionKey, keys]) => (
          keys.length > 0 ? (
            <SectionBlock
              key={sectionKey}
              title={sectionTitle(sectionKey)}
              settings={settings}
              keys={keys}
              onApply={handleApply}
              saving={saving}
            />
          ) : null
        ))}
      </div>

      <RevisionConflictModal
        conflict={conflict}
        onClose={() => setConflict(null)}
        onDiscard={handleDiscard}
        onForceSave={handleForceSave}
      />
    </>
  );
}

function SectionBlock({ title, settings, keys, onApply, saving }) {
  return (
    <section className="card" style={{ marginTop: 12 }}>
      <h3 className="card__title">{title}</h3>
      <div className="form-grid">
        {keys.map((key) => (
          <FieldRow
            key={key}
            path={key}
            value={getByPath(settings, key)}
            onApply={onApply}
            saving={saving}
          />
        ))}
      </div>
    </section>
  );
}

function FieldRow({ path, value, onApply, saving }) {
  const inputType = inferInputType(value, path);
  const label = path;

  if (inputType === 'bool') {
    return (
      <label className="form__row">
        <span className="form__label">{label}</span>
        <span className="row gap-sm" style={{ cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={!!value}
            disabled={saving}
            onChange={(e) => onApply({ [path]: e.target.checked })}
            style={{ width: 'auto' }}
          />
          <span className="dim">{value ? 'enabled' : 'disabled'}</span>
        </span>
      </label>
    );
  }

  if (inputType === 'number') {
    return (
      <label className="form__row">
        <span className="form__label">{label}</span>
        <input
          type="number"
          defaultValue={value ?? ''}
          disabled={saving}
          onBlur={(e) => {
            const raw = e.target.value;
            const num = raw === '' ? 0 : Number(raw);
            if (Number.isFinite(num) && num !== value) {
              onApply({ [path]: num });
            }
          }}
        />
      </label>
    );
  }

  if (inputType === 'password') {
    return (
      <label className="form__row">
        <span className="form__label">{label}</span>
        <input
          type="password"
          defaultValue={value ?? ''}
          disabled={saving}
          autoComplete="off"
          onBlur={(e) => {
            const next = e.target.value;
            if (next !== value) onApply({ [path]: next });
          }}
        />
      </label>
    );
  }

  if (inputType === 'text') {
    return (
      <label className="form__row">
        <span className="form__label">{label}</span>
        <input
          type="text"
          defaultValue={value ?? ''}
          disabled={saving}
          onBlur={(e) => {
            const next = e.target.value;
            if (next !== value) onApply({ [path]: next });
          }}
        />
      </label>
    );
  }

  // object / array / json: render as a JSON textarea. On blur, attempt to
  // parse; if parsing fails, the field stays unsaved and an inline hint tells
  // the operator what went wrong. We never silently throw away their input.
  if (inputType === 'object' || inputType === 'array' || inputType === 'json') {
    return <JsonFieldRow path={path} value={value} onApply={onApply} saving={saving} />;
  }

  return null;
}

function JsonFieldRow({ path, value, onApply, saving }) {
  const initial = value === undefined || value === null ? '' : JSON.stringify(value, null, 2);
  const [draft, setDraft] = useState(initial);
  const [parseError, setParseError] = useState(null);

  // Keep draft in sync when the parent snapshot reloads with new data.
  React.useEffect(() => {
    setDraft(initial);
    setParseError(null);
  }, [initial]);

  return (
    <label className="form__row" style={{ gridColumn: '1 / -1' }}>
      <span className="form__label">{path}</span>
      <textarea
        rows={Math.min(12, Math.max(3, draft.split('\n').length))}
        value={draft}
        disabled={saving}
        onChange={(e) => { setDraft(e.target.value); setParseError(null); }}
        onBlur={() => {
          if (draft === initial) return;
          try {
            const parsed = draft.trim() === '' ? null : JSON.parse(draft);
            onApply({ [path]: parsed });
          } catch (err) {
            setParseError(`Invalid JSON: ${err.message}`);
          }
        }}
        style={{ fontFamily: 'var(--font-mono, monospace)', fontSize: 12 }}
      />
      {parseError && <span className="dim" style={{ color: 'var(--danger, #e54848)', fontSize: 12 }}>{parseError}</span>}
    </label>
  );
}
