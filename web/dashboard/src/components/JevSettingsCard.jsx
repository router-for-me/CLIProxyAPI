import React, { useEffect, useState } from 'react';
import { getJevSettings, putJevSettings } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner } from './Primitives.jsx';
import { useToast } from './Toast.jsx';

// JevSettingsCard edits the Jev AI classifier settings: the global master
// switch, the API key, and the pinned classifier model. It appears under
// Settings because it is a single global configuration, not a per-router one —
// a router opts in separately on its own form.
//
// Two properties of the API shape drive this component:
//
//   - The key is write-only. GET returns `api_key_set` and `api_key_prefix`
//     (a masked head of the key) but never the key itself, so the input starts
//     empty and a blank input means "keep what is stored".
//   - `api_key` is tri-state on the wire: omitted keeps the stored key, "" or
//     null clears it, and any other value rotates it. The component therefore
//     sends the field only when the operator actually typed something, and
//     clearing is an explicit action rather than a side effect of a blank box.
function JevSettingsCard() {
  const toast = useToast();
  const settingsReq = useAsync(() => getJevSettings(), []);
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState(null);
  // apiKey is deliberately not part of `draft`: it is never read back from the
  // server, so it must not be clobbered when the settings reload.
  const [apiKey, setApiKey] = useState('');

  useEffect(() => {
    if (settingsReq.data?.settings) setDraft(settingsReq.data.settings);
  }, [settingsReq.data]);

  function set(patch) { setDraft((d) => ({ ...d, ...patch })); }

  async function handleSave() {
    setSaving(true);
    try {
      const body = {
        enabled: !!draft.enabled,
        model: String(draft.model || '').trim(),
      };
      // Only send the key when one was typed: omitting it preserves whatever is
      // stored, which is what "leave the box blank" should mean.
      if (apiKey.trim()) body.api_key = apiKey.trim();

      await putJevSettings(body);
      setApiKey('');
      toast.success('Jev AI settings saved');
      settingsReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to save Jev AI settings');
    } finally {
      setSaving(false);
    }
  }

  async function handleClearKey() {
    if (!window.confirm('Remove the stored Jev AI API key? Classification stops until a new key is saved.')) return;
    setSaving(true);
    try {
      await putJevSettings({ api_key: '' });
      setApiKey('');
      toast.success('Jev AI API key removed');
      settingsReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to remove the Jev AI API key');
    } finally {
      setSaving(false);
    }
  }

  const keySet = !!draft?.api_key_set;

  return (
    <div className="card">
      <h3 className="card__title" style={{ margin: 0 }}>Jev AI classification</h3>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Classifies each auto-routed request before the heuristic tier is applied,
        and routes by the classifier&apos;s tier when its confidence clears the
        router&apos;s threshold. Auto Routers opt in individually; with this switch
        off the classifier is never called.
      </p>

      {settingsReq.loading && !draft && <Spinner label="Loading Jev AI settings…" />}
      {settingsReq.error && !draft && <ErrorBanner error={settingsReq.error} onRetry={settingsReq.reload} />}
      {draft && (
        <>
          <label className="row gap-sm" style={{ cursor: 'pointer', marginBottom: 16 }}>
            <input type="checkbox" checked={!!draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} style={{ width: 'auto' }} />
            <span className="form__label" style={{ margin: 0 }}>Jev AI classification enabled</span>
          </label>

          <div className="grid grid--2">
            <label className="form__row">
              <span className="form__label">Classifier model</span>
              <input
                type="text"
                value={draft.model ?? ''}
                placeholder="jev-1.13.0"
                onChange={(e) => set({ model: e.target.value })}
              />
            </label>
            <label className="form__row">
              <span className="form__label">API key</span>
              <input
                type="password"
                autoComplete="off"
                value={apiKey}
                placeholder={keySet ? `stored: ${draft.api_key_prefix || '••••'}` : 'not set'}
                onChange={(e) => setApiKey(e.target.value)}
              />
            </label>
          </div>

          <p className="muted" style={{ marginTop: 8 }}>
            The key is stored encrypted and is never returned by the API. Leave the
            field blank to keep the current key; type a new one to replace it. A
            saved key is adopted by the running server immediately.
          </p>

          <div className="form__actions" style={{ marginTop: 16 }}>
            <button onClick={handleSave} disabled={saving}>{saving ? 'Saving…' : 'Save Jev AI settings'}</button>
            {keySet && (
              <button className="danger" onClick={handleClearKey} disabled={saving}>Remove key</button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

export default JevSettingsCard;
