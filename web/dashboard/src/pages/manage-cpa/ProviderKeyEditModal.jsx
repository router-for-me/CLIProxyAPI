// Provider-key edit modal — schema-aware form for adding/editing one entry
// in any of the seven provider-key lists (Gemini, Interactions, Claude,
// Codex, xAI, Vertex, OpenAI-Compat).
//
// The modal is intentionally large (size="xl") because the server's
// per-provider schemas have a lot of fields and the previous cramped 520px
// form hid most of them. Form state is local until submit; the parent
// receives the final payload via onSubmit and decides which endpoint
// (PUT full list or PATCH one row) to call.
//
// The validation layer is intentionally light: required fields, URL shape
// for endpoints, and a uniqueness check on OpenAI-Compat's `name`. The
// server still re-validates the YAML after the round-trip, so client-side
// validation is purely to give the operator faster feedback.

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Modal } from '../../components/Primitives.jsx';
import {
  Field,
  PasswordInput,
  ToggleRow,
  ChipListEditor,
  KeyValueEditor,
  ModelListEditor,
} from './FormPrimitives.jsx';
import FetchModelsInline from './FetchModelsInline.jsx';

// --- Per-provider schema --------------------------------------------------
//
// Each schema entry tells the form:
//   - which sub-sections to render (and in what order),
//   - which fields go in each section,
//   - what input type each field uses,
//   - any client-side validation,
//   - the field help text.
//
// Keeping the schema declarative means adding a new provider is a single
// object literal rather than a new render branch in the component.
const URL_RE = /^(https?:\/\/|socks5h?:\/\/)/i;

function buildSchemas() {
  const commonEndpoint = [
    {
      name: 'base_url',
      label: 'Base URL',
      type: 'text',
      placeholder: 'https://api.example.com',
      hint: 'Upstream API base URL. Leave blank to use the provider default.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://' : ''),
    },
    {
      name: 'proxy_url',
      label: 'Proxy URL',
      type: 'text',
      placeholder: 'socks5://user:pass@host:1080',
      hint: 'Per-key proxy override. Empty uses the global proxy-url from config.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://' : ''),
    },
    {
      name: 'prefix',
      label: 'Model prefix',
      type: 'text',
      placeholder: 'teamA/',
      hint: 'Optional namespace prepended to every model this entry serves.',
    },
  ];

  const commonRouting = [
    {
      name: 'priority',
      label: 'Priority',
      type: 'number',
      min: 0,
      placeholder: '0',
      hint: 'Higher value is preferred when multiple credentials match.',
    },
    {
      name: 'models',
      label: 'Models',
      type: 'models',
      hint: 'Map client-facing aliases to upstream model names.',
    },
    {
      name: 'excluded_models',
      label: 'Excluded models',
      type: 'chips',
      placeholder: 'gpt-4o, claude-3-haiku',
      hint: 'Models that should never be routed through this entry.',
    },
  ];

  const commonBehavior = [
    {
      name: 'headers',
      label: 'Custom headers',
      type: 'headers',
      hint: 'Extra HTTP headers attached to every request using this entry.',
    },
    {
      name: 'disabled',
      label: 'Disabled',
      type: 'toggle',
      hint: 'When on, this entry is excluded from routing without removing it.',
    },
    {
      name: 'disable_cooling',
      label: 'Disable cooldown',
      type: 'toggle',
      hint: 'Skip the cooldown schedule when this credential hits an error.',
    },
  ];

  return {
    gemini: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential that authenticates Gemini API requests.',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'AIza…',
              required: true,
              hint: 'A Google AI Studio / Gemini API key.',
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: commonBehavior },
      ],
      // Gemini uses hyphenated JSON keys identical to its form names, so
      // the payload is essentially the form object. We just drop empties.
      toPayload: (f) => compactPayload(f, [
        'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
        'excluded-models',
      ]),
    },

    interactions: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential that authenticates Interactions API requests.',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'AIza…',
              required: true,
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: commonBehavior },
      ],
      toPayload: (f) => compactPayload(f, [
        'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
        'excluded-models',
      ]),
    },

    claude: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential that authenticates Claude API requests.',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'sk-ant-…',
              required: true,
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: [
          ...commonBehavior,
          {
            name: 'rebuild_mid_system_message',
            label: 'Rebuild mid system message',
            type: 'toggle',
            hint: 'Move system-role messages into the top-level system field.',
          },
          {
            name: 'experimental_cch_signing',
            label: 'Experimental CCH signing',
            type: 'toggle',
            hint: 'Opt-in final-body cch signing for cloaked Claude /v1/messages requests.',
          },
        ] },
        {
          title: 'Cloak',
          hint: 'Request cloaking for non-Claude-Code clients (Claude only).',
          fields: [
            {
              name: 'cloak.enabled',
              label: 'Enable cloak',
              type: 'toggle',
              hint: 'When on, outgoing requests are rewritten to look like Claude-Code.',
            },
            {
              name: 'cloak.mode',
              label: 'Cloak mode',
              type: 'select',
              options: [
                { value: '', label: '— default —' },
                { value: 'auto', label: 'auto' },
                { value: 'strict', label: 'strict' },
                { value: 'passthrough', label: 'passthrough' },
              ],
              hint: 'How aggressively to rewrite the request envelope.',
            },
            {
              name: 'cloak.sensitive_words',
              label: 'Sensitive words',
              type: 'chips',
              placeholder: 'add a word, press Enter',
              hint: 'Words that should never appear in the rewritten request.',
            },
          ],
        },
      ],
      toPayload: (f) => {
        const out = compactPayload(f, [
          'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
          'excluded-models',
        ]);
        // Merge cloak sub-fields if any are set.
        const cloak = buildCloakPayload(f);
        if (cloak) out.cloak = cloak;
        return out;
      },
    },

    codex: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential that authenticates Codex / OpenAI API requests.',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'sk-…',
              required: true,
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: commonBehavior },
      ],
      toPayload: (f) => compactPayload(f, [
        'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
        'excluded-models',
      ]),
    },

    xai: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential that authenticates xAI API requests.',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'xai-…',
              required: true,
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: [
          ...commonBehavior,
          {
            name: 'websockets',
            label: 'WebSockets',
            type: 'toggle',
            hint: 'Use the Responses API websocket transport for this entry.',
          },
        ] },
      ],
      toPayload: (f) => compactPayload(f, [
        'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
        'excluded-models', 'websockets',
      ]),
    },

    vertex: {
      sections: [
        {
          title: 'Identity',
          hint: 'The credential for a Vertex-AI-compatible provider (zenmux, etc).',
          fields: [
            {
              name: 'api_key',
              label: 'API key',
              type: 'password',
              placeholder: 'sk-…',
              required: true,
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting },
        { title: 'Behavior', fields: [
          {
            name: 'headers',
            label: 'Custom headers',
            type: 'headers',
            hint: 'Extra HTTP headers (e.g. cookies) attached to every request.',
          },
          {
            name: 'disable_cooling',
            label: 'Disable cooldown',
            type: 'toggle',
            hint: 'Skip the cooldown schedule when this credential hits an error.',
          },
        ] },
      ],
      toPayload: (f) => compactPayload(f, [
        'api-key', 'base-url', 'proxy-url', 'prefix', 'priority',
        'excluded-models',
      ]),
    },

    openai: {
      sections: [
        {
          title: 'Identity',
          hint: 'OpenAI-compatible provider (openrouter, ollama, litellm, etc).',
          fields: [
            {
              name: 'name',
              label: 'Provider name',
              type: 'text',
              placeholder: 'openrouter',
              required: true,
              hint: 'A unique identifier used in oauth-model-alias and the URL path.',
            },
            {
              name: 'api_key',
              label: 'First API key',
              type: 'password',
              placeholder: 'sk-…',
              hint: 'You can add more keys later by editing the raw YAML.',
            },
          ],
        },
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: [
          {
            name: 'priority',
            label: 'Priority',
            type: 'number',
            min: 0,
            placeholder: '0',
            hint: 'Higher value is preferred when multiple providers match.',
          },
          {
            name: 'models',
            label: 'Models',
            type: 'openai_models',
            hint: 'Map client-facing aliases to upstream model names.',
          },
        ] },
        { title: 'Behavior', fields: [
          {
            name: 'headers',
            label: 'Custom headers',
            type: 'headers',
            hint: 'Extra HTTP headers attached to every request to this provider.',
          },
          {
            name: 'disabled',
            label: 'Disabled',
            type: 'toggle',
            hint: 'When on, this provider is excluded from routing.',
          },
          {
            name: 'disable_cooling',
            label: 'Disable cooldown',
            type: 'toggle',
            hint: 'Skip the cooldown schedule when this provider hits an error.',
          },
        ] },
      ],
      toPayload: (f) => {
        const out = {};
        if (f.name?.trim()) out.name = f.name.trim();
        if (f.base_url?.trim()) out['base-url'] = f.base_url.trim();
        if (f.prefix?.trim()) out.prefix = f.prefix.trim();
        const prio = parseIntSafe(f.priority);
        if (prio !== null) out.priority = prio;
        out.disabled = !!f.disabled;
        out['disable-cooling'] = !!f.disable_cooling;
        const headers = nonEmptyKeyValue(f.headers);
        if (headers) out.headers = headers;
        const models = cleanModelList(f.models, { keepOpenAI: true });
        if (models) out.models = models;
        if (f.api_key?.trim()) {
          out['api-key-entries'] = [{ 'api-key': f.api_key.trim() }];
        }
        return out;
      },
    },
  };
}

// --- helpers --------------------------------------------------------------

// compactPayload drops empty strings and undefined values, then maps form
// field names to the server's hyphenated JSON keys. The server expects
// fields like "api-key", "base-url", "excluded-models" — the form uses the
// underscored versions so the form-state key matches the render helper.
function compactPayload(form, hyphenFields) {
  const out = {};
  for (const f of hyphenFields) {
    const formKey = f.replace(/-/g, '_');
    const v = form[formKey];
    if (isMeaningful(v)) out[f] = v;
  }
  const prio = parseIntSafe(form.priority);
  if (prio !== null) out.priority = prio;
  if (form.websockets) out.websockets = true;
  if (form.rebuild_mid_system_message) out['rebuild-mid-system-message'] = true;
  if (form.experimental_cch_signing) out['experimental-cch-signing'] = true;
  if (form.disable_cooling) out['disable-cooling'] = true;

  const headers = nonEmptyKeyValue(form.headers);
  if (headers) out.headers = headers;

  const models = cleanModelList(form.models);
  if (models) out.models = models;

  return out;
}

function buildCloakPayload(form) {
  const enabled = !!form['cloak.enabled'];
  const mode = (form['cloak.mode'] || '').trim();
  const words = Array.isArray(form['cloak.sensitive_words'])
    ? form['cloak.sensitive_words'].filter(Boolean)
    : [];
  if (!enabled && !mode && words.length === 0) return null;
  const out = {};
  if (enabled) out.enabled = true;
  if (mode) out.mode = mode;
  if (mode === 'strict') {
    out['strict-mode'] = true;
  }
  if (words.length > 0) out['sensitive-words'] = words;
  return out;
}

function parseIntSafe(v) {
  if (v === '' || v === null || v === undefined) return null;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : null;
}

function nonEmptyKeyValue(rows) {
  if (!Array.isArray(rows)) return null;
  const out = {};
  for (const r of rows) {
    if (r && r.key && r.key.trim()) {
      out[r.key.trim()] = r.value || '';
    }
  }
  return Object.keys(out).length > 0 ? out : null;
}

function cleanModelList(rows, opts = {}) {
  if (!Array.isArray(rows)) return null;
  const cleaned = rows
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const o = {};
      if (r.name?.trim()) o.name = r.name.trim();
      if (r.alias?.trim()) o.alias = r.alias.trim();
      if (r['display-name']?.trim()) o['display-name'] = r['display-name'].trim();
      if (r['force-mapping']) o['force-mapping'] = true;
      if (opts.keepOpenAI) {
        if (Array.isArray(r['input-modalities']) && r['input-modalities'].length > 0) {
          o['input-modalities'] = r['input-modalities'];
        }
        if (Array.isArray(r['output-modalities']) && r['output-modalities'].length > 0) {
          o['output-modalities'] = r['output-modalities'];
        }
        if (r.image) o.image = true;
      }
      return o;
    });
  return cleaned.length > 0 ? cleaned : null;
}

function isMeaningful(v) {
  if (v === undefined || v === null) return false;
  if (typeof v === 'string') return v.trim() !== '';
  if (Array.isArray(v)) return v.length > 0;
  if (typeof v === 'object') return Object.keys(v).length > 0;
  return true;
}

// --- Initial-form construction -------------------------------------------

function buildForm(provider, initial) {
  const base = {
    api_key: '',
    base_url: '',
    proxy_url: '',
    prefix: '',
    priority: '',
    models: [{ name: '', alias: '' }],
    excluded_models: [],
    headers: [],
    disabled: false,
    disable_cooling: false,
    websockets: false,
    rebuild_mid_system_message: false,
    experimental_cch_signing: false,
    'cloak.enabled': false,
    'cloak.mode': '',
    'cloak.sensitive_words': [],
    name: '',
  };
  if (!initial) return base;
  // The CPA server returns JSON keys with hyphens (api-key, base-url).
  // Some object-spread paths or upstream serializers occasionally flip
  // the casing or flatten the key — accept both shapes as a defensive
  // measure so the form always populates when the data is present.
  const pickStr = (...keys) => {
    for (const k of keys) {
      if (initial[k] != null && initial[k] !== '') return initial[k];
    }
    return '';
  };
  const out = {
    ...base,
    api_key: pickStr('api-key', 'api_key'),
    base_url: pickStr('base-url', 'base_url'),
    proxy_url: pickStr('proxy-url', 'proxy_url'),
    prefix: pickStr('prefix'),
    priority: initial.priority != null ? String(initial.priority) : '',
    disabled: !!initial.disabled,
    disable_cooling: !!initial['disable-cooling'] || !!initial.disable_cooling,
    websockets: !!initial.websockets,
    rebuild_mid_system_message: !!initial['rebuild-mid-system-message'] || !!initial.rebuild_mid_system_message,
    experimental_cch_signing: !!initial['experimental-cch-signing'] || !!initial.experimental_cch_signing,
  };
  if (Array.isArray(initial.models) && initial.models.length > 0) {
    out.models = initial.models.map((m) => ({
      name: m.name || '',
      alias: m.alias || '',
      'display-name': m['display-name'] || '',
      'force-mapping': !!m['force-mapping'],
      image: !!m.image,
      'input-modalities': m['input-modalities'] || [],
      'output-modalities': m['output-modalities'] || [],
    }));
  }
  if (Array.isArray(initial['excluded-models'])) {
    out.excluded_models = initial['excluded-models'].slice();
  }
  if (initial.headers && typeof initial.headers === 'object') {
    out.headers = Object.entries(initial.headers).map(([key, value]) => ({ key, value: String(value) }));
  }
  if (provider === 'openai') {
    out.name = initial.name || '';
    // OpenAI-Compat stores keys in api-key-entries[]; accept either the
    // kebab-case wire format (current server json tag), the legacy
    // snake_case mirror, or a single api-key field (older hand-edited
    // configs).
    const entries =
      Array.isArray(initial['api-key-entries']) ? initial['api-key-entries']
        : Array.isArray(initial.api_key_entries) ? initial.api_key_entries
          : null;
    const firstEntry = entries && entries.length > 0 ? entries[0] : null;
    out.api_key = firstEntry?.['api-key']
      || firstEntry?.api_key
      || initial['api-key']
      || initial.api_key
      || '';
  }
  if (provider === 'claude' && initial.cloak && typeof initial.cloak === 'object') {
    out['cloak.enabled'] = !!initial.cloak.enabled;
    out['cloak.mode'] = initial.cloak.mode || '';
    if (Array.isArray(initial.cloak['sensitive-words'])) {
      out['cloak.sensitive_words'] = initial.cloak['sensitive-words'].slice();
    }
  }
  return out;
}

// --- The modal ------------------------------------------------------------

export default function ProviderKeyEditModal({
  kind,
  mode,
  initial,
  // List of existing names, used for the OpenAI-Compat name uniqueness check.
  siblingNames = [],
  onClose,
  onSaved,
  onSubmit,
}) {
  const schemas = useMemo(() => buildSchemas(), []);
  const schema = schemas[kind.id] || schemas.gemini;
  const isEdit = mode === 'edit';

  const [form, setForm] = useState(() => buildForm(kind.id, initial));
  const [touched, setTouched] = useState({});
  const [submitting, setSubmitting] = useState(false);
  const [serverError, setServerError] = useState('');
  const [initialSnapshot] = useState(() => JSON.stringify(form));
  const [closingForCancel, setClosingForCancel] = useState(false);
  const initialRef = useRef(initial);

  // Re-derive the form whenever `initial` actually points at a different
  // backing entry. We intentionally key on the *first* api_key + base_url
  // (and the entry's name for OpenAI-Compat) instead of the object
  // reference, so a fresh list reload that returns structurally-equal
  // entries (e.g. after a refetch) doesn't wipe the operator's in-flight
  // edits. Conversely, an Edit on a different row always produces a
  // different identity key and forces a re-init, even if React would
  // otherwise reuse the same component instance because the parent
  // didn't change `editing` between mount and re-mount.
  const initialIdentity = useMemo(() => {
    if (!initial) return '';
    // Try both kebab-case (current Go json tag) and snake_case (legacy)
    // when reading the first configured api-key for an OpenAI-Compat
    // entry. Same defensive style as buildForm above.
    const openaiEntries =
      Array.isArray(initial['api-key-entries']) ? initial['api-key-entries']
        : Array.isArray(initial.api_key_entries) ? initial.api_key_entries
          : null;
    const openaiFirstKey = openaiEntries?.[0]?.['api-key'] || '';
    const providerKey = initial['api-key'] || initial.api_key || openaiFirstKey || '';
    const baseUrl = initial.base_url || '';
    const name = initial.name || '';
    return `${kind.id}|${providerKey}|${baseUrl}|${name}`;
  }, [initial, kind.id]);

  // Initialise on first mount AND whenever the identity changes (i.e.
  // a genuinely different row was selected). We compare against the
  // previous identity with a ref so we can detect the transition.
  const prevIdentityRef = useRef('');
  useEffect(() => {
    if (!initial) return;
    if (prevIdentityRef.current === initialIdentity) return;
    prevIdentityRef.current = initialIdentity;
    setForm(buildForm(kind.id, initial));
    setTouched({});
    setServerError('');
    initialRef.current = initial;
  }, [initialIdentity, initial, kind.id]);

  // Reset form when `initial` changes (e.g. switching rows in the same
  // modal session — currently not possible, but defensive).
  useEffect(() => {
    if (initialRef.current !== initial) {
      initialRef.current = initial;
      setForm(buildForm(kind.id, initial));
    }
  }, [initial, kind.id]);

  const errors = useMemo(() => validate(form, schema, kind.id, siblingNames, isEdit), [
    form, schema, kind.id, siblingNames, isEdit,
  ]);
  const hasErrors = Object.keys(errors).length > 0;

  const dirty = JSON.stringify(form) !== initialSnapshot;

  function update(name, value) {
    setForm((f) => ({ ...f, [name]: value }));
    setTouched((t) => ({ ...t, [name]: true }));
    setServerError('');
  }

  async function handleSubmit(e) {
    e?.preventDefault?.();
    if (hasErrors) {
      setTouched(Object.fromEntries(Object.keys(errors).map((k) => [k, true])));
      return;
    }
    setSubmitting(true);
    setServerError('');
    try {
      const payload = schema.toPayload(form);
      await onSubmit(payload);
      onSaved();
    } catch (err) {
      setServerError(err.message || 'Save failed.');
    } finally {
      setSubmitting(false);
    }
  }

  function attemptClose() {
    if (dirty && !submitting && !closingForCancel) {
      const ok = window.confirm('Discard unsaved changes?');
      if (!ok) return;
    }
    onClose();
  }

  const title = isEdit
    ? `Edit ${kind.label} entry`
    : `Add ${kind.label} entry`;

  return (
    <Modal
      title={title}
      size="xl"
      onClose={attemptClose}
      footer={
        <>
          <button type="button" onClick={attemptClose} disabled={submitting}>
            Cancel
          </button>
          <button
            type="button"
            className="primary"
            onClick={handleSubmit}
            disabled={submitting || (hasErrors && isEdit)}
          >
            {submitting ? 'Saving…' : isEdit ? 'Save changes' : 'Add entry'}
          </button>
        </>
      }
    >
      {/* No <form> wrapper on purpose: the Save button is rendered in the
          Modal's footer (outside this container), so Enter in a field
          would never trigger submit through normal form semantics.
          A <form> element would, however, capture Enter presses anywhere
          inside it — and any untyped <button> descendant (e.g. the Fetch
          button inside FetchModelsInline) would default to type="submit"
          and silently close the modal. Using a plain <div> keeps Enter
          submissions, button-type defaults, and the Fetch picker all
          working independently. The Save button invokes handleSubmit
          directly via onClick. */}
      <div>
        {serverError && <div className="error-banner">{serverError}</div>}
        {hasErrors && (
          <div className="error-summary">
            <strong>Please fix {Object.keys(errors).length} field{Object.keys(errors).length === 1 ? '' : 's'}:</strong>
            <ul>
              {Object.entries(errors).map(([k, v]) => (
                <li key={k}>{v}</li>
              ))}
            </ul>
          </div>
        )}

        {schema.sections.map((section) => (
          <div className="form-section" key={section.title}>
            <div className="form-section__title">{section.title}</div>
            {section.hint && <div className="form-section__hint">{section.hint}</div>}
            <div className="form-section__row">
              {section.fields.map((field) => {
                const showError = touched[field.name] && errors[field.name];
                return (
                  <Field
                    key={field.name}
                    label={field.label}
                    hint={field.hint}
                    error={showError ? errors[field.name] : ''}
                    required={field.required}
                    htmlFor={`f_${field.name.replace(/[.\s]/g, '_')}`}
                  >
                    {renderInput(field, form, update, isEdit)}
                  </Field>
                );
              })}
            </div>
            {/* Inline Fetch Models sub-section — rendered at the bottom of
                the Routing section so the operator can probe upstream and
                append the discovered models into the same form field. The
                component is self-contained; it only writes to the parent
                form via the onAddModels callback. */}
            {section.title === 'Routing' && (
              <FetchModelsInline
                form={form}
                provider={kind.id}
                isEdit={isEdit}
                siblingNames={siblingNames}
                onAddModels={(picked) => {
                  // Merge picks into the existing form.models list. We
                  // dedupe by `name` and drop empty picker rows. Existing
                  // rows are kept verbatim so manual edits aren't lost.
                  const existing = Array.isArray(form.models) ? form.models : [];
                  const byName = new Set(
                    existing
                      .map((r) => (r?.name || '').trim())
                      .filter(Boolean),
                  );
                  const additions = picked
                    .filter((p) => p && p.id && !byName.has(p.id))
                    .map((p) => ({
                      name: p.id,
                      ...(p.display_name ? { 'display-name': p.display_name } : {}),
                    }));
                  if (additions.length > 0) {
                    update('models', [...existing, ...additions]);
                  }
                }}
              />
            )}
          </div>
        ))}
      </div>
    </Modal>
  );
}

// --- Input rendering ------------------------------------------------------

function renderInput(field, form, update, isEdit = false) {
  const id = `f_${field.name.replace(/[.\s]/g, '_')}`;
  const value = form[field.name];
  const errClass = '';
  switch (field.type) {
    case 'password':
      return (
        <PasswordInput
          id={id}
          value={value}
          onChange={(v) => update(field.name, v)}
          placeholder={field.placeholder}
          // In Edit mode the operator already has full management access,
          // so show the saved value in plain text. They can still toggle
          // to mask it via the Show/Hide button. In Add mode keep the
          // default masking so partially-typed secrets aren't exposed
          // over the operator's shoulder.
          defaultShown={isEdit}
        />
      );
    case 'number':
      return (
        <input
          id={id}
          type="number"
          min={field.min}
          value={value ?? ''}
          onChange={(e) => update(field.name, e.target.value)}
          placeholder={field.placeholder}
        />
      );
    case 'select':
      return (
        <select
          id={id}
          value={value || ''}
          onChange={(e) => update(field.name, e.target.value)}
        >
          {field.options.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
      );
    case 'toggle':
      return (
        <ToggleRow
          label={field.label}
          hint={field.hint}
          checked={!!value}
          onChange={(v) => update(field.name, v)}
        />
      );
    case 'chips':
      return (
        <ChipListEditor
          values={value || []}
          onChange={(v) => update(field.name, v)}
          placeholder={field.placeholder}
          emptyHint={field.emptyHint}
        />
      );
    case 'headers':
      return (
        <KeyValueEditor
          rows={value || []}
          onChange={(v) => update(field.name, v)}
        />
      );
    case 'models':
      return (
        <ModelListEditor
          rows={value || []}
          onChange={(v) => update(field.name, v)}
          fieldHints={{ name: 'upstream name', alias: 'client alias', displayName: 'display name' }}
        />
      );
    case 'openai_models':
      return (
        <ModelListEditor
          rows={value || []}
          onChange={(v) => update(field.name, v)}
          fieldHints={{
            name: 'upstream model (e.g. anthropic/claude-3-5-sonnet)',
            alias: 'client alias (e.g. claude-sonnet)',
            displayName: 'display name (optional)',
          }}
        />
      );
    case 'text':
    default:
      return (
        <input
          id={id}
          type="text"
          value={value || ''}
          onChange={(e) => update(field.name, e.target.value)}
          placeholder={field.placeholder}
          required={field.required}
        />
      );
  }
}

// --- Validation -----------------------------------------------------------

function validate(form, schema, providerId, siblingNames, isEdit) {
  const errors = {};
  // Walk every field in every section.
  for (const section of schema.sections) {
    for (const field of section.fields) {
      const v = form[field.name];
      // Required check.
      if (field.required && (v === '' || v === null || v === undefined)) {
        errors[field.name] = `${field.label} is required.`;
        continue;
      }
      // Custom validator (returns a string error or empty string).
      if (field.validate && v) {
        const msg = field.validate(v);
        if (msg) errors[field.name] = msg;
      }
    }
  }
  // Provider-specific checks.
  if (providerId === 'openai') {
    const name = (form.name || '').trim();
    if (name) {
      const conflict = siblingNames.some(
        (s) => s && s !== isEdit ? false : (s === name),
      );
      // Better: just compare against the siblings minus the current row.
      const others = (siblingNames || []).filter((s) => s !== (isEdit ? name : null));
      if (others.includes(name)) {
        errors.name = `Another OpenAI-Compat entry already uses "${name}".`;
      }
    }
  }
  return errors;
}
