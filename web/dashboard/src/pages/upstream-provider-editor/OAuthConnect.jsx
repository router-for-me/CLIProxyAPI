// ============================================================================
// Upstream provider editor — OAuth connect section
// ============================================================================

// OAuthConnectSection — the connect workflow step for oauth:* providers.
// Extracted from UpstreamProvidersPage.jsx (provider-editor-page plan,
// Task 3); rendered by the routed editor page (./index.jsx) for connectable
// OAuth types in create mode.

import React, { useState } from 'react';
import {
  requestOAuthUrl,
  submitOAuthCallback,
  oauthChannelToAuthProvider,
  listAuthFiles,
  getAuthFileModels,
  fetchAuthFileJSON,
} from '../../api/client.js';
import { Spinner } from '../../components/Primitives.jsx';
import { useToast } from '../../components/Toast.jsx';
import { TYPE_LABEL } from './schemas.js';

// strVal safely extracts a trimmed string from an object by key. Returns ''
// for missing/null/non-string values. Local copy of the helper that still
// lives in UpstreamProvidersPage.jsx (used there by the import modal's
// extractAuthFields) — the codebase's convention for tiny helpers is a
// duplicated local definition rather than a shared module (see capitalize
// in ModelRouteConfigSection.jsx).
function strVal(obj, key) {
  if (!obj || typeof obj !== 'object') return '';
  const v = obj[key];
  if (typeof v === 'string') return v.trim();
  if (typeof v === 'number') return String(v);
  return '';
}

// ============================================================================
// OAuthConnectSection — the connect workflow step for oauth:* providers
// ============================================================================

// OAuthConnectSection renders the OAuth authorization workflow:
//   1. Generate authorize URL (calls /<provider>-auth-url)
//   2. Auto-open the URL in a new tab (+ copy button fallback)
//   3. Operator completes login in their browser
//   4. Operator pastes the callback redirect URL (or raw code+state)
//   5. Submit calls /oauth-callback to complete the token exchange
//
// After a successful callback, onCompleted() is called so the parent can
// reveal the full identity/token settings. This section is shown ABOVE the
// schema-driven sections for oauth:* types, so the operator follows the
// connect flow first before configuring further details.
export default function OAuthConnectSection({ providerType, onCompleted }) {
  const toast = useToast();
  const channel = providerType.replace(/^oauth:/, '');
  const authProvider = oauthChannelToAuthProvider(channel);

  const [stage, setStage] = useState('idle'); // idle | loading | ready | submitting | done | error
  const [error, setError] = useState('');
  const [authUrl, setAuthUrl] = useState('');
  const [oauthState, setOauthState] = useState('');
  const [flow, setFlow] = useState('web');
  const [userCode, setUserCode] = useState('');
  const [callbackUrl, setCallbackUrl] = useState('');
  const [copied, setCopied] = useState(false);

  const handleGenerate = () => {
    setStage('loading');
    setError('');
    setAuthUrl('');
    setCallbackUrl('');
    requestOAuthUrl(authProvider, { isWebUI: true })
      .then((res) => {
        setStage('ready');
        setAuthUrl(res?.url || '');
        setOauthState(res?.state || '');
        setFlow(res?.flow || 'web');
        setUserCode(res?.user_code || '');
        // Auto-open the authorize URL in a new tab so the operator can start
        // the browser login immediately.
        if (res?.url) {
          window.open(res.url, '_blank', 'noopener,noreferrer');
        }
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Failed to generate authorize URL');
      });
  };

  const handleSubmitCallback = () => {
    const trimmed = callbackUrl.trim();
    if (!trimmed) {
      toast.error('Paste the callback redirect URL first.');
      return;
    }
    setStage('submitting');
    setError('');
    submitOAuthCallback({ provider: authProvider, redirectUrl: trimmed })
      .then(() => {
        setStage('fetching');
        toast.info('OAuth callback accepted. Fetching credential details…');
        // The server persists the auth file asynchronously + a background
        // waiter exchanges the code for tokens. Poll listAuthFiles until the
        // new auth file for this provider channel appears, then extract the
        // identity (email/file_name/label) and discover its models.
        pollForAuthFile(0)
          .then((authData) => {
            setStage('done');
            toast.success('OAuth flow completed. Provider details populated.');
            onCompleted?.(authData);
          })
          .catch((err) => {
            // Even if polling fails/times out, mark as done so the operator
            // can proceed manually — the credential may still be settling.
            setStage('done');
            toast.info(err.message || 'Could not auto-fetch details; fill them manually.');
            onCompleted?.(null);
          });
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Callback submission failed');
        toast.error(err.message || 'Callback submission failed');
      });
  };

  // pollForAuthFile polls listAuthFiles up to maxAttempts (every 1.5s) until
  // it finds a non-disabled auth file whose type matches the OAuth channel.
  // Once found, it fetches the file's models via getAuthFileModels and
  // returns {file_name, email, label, models}.
  const pollForAuthFile = (attempt) => {
    const maxAttempts = 8;
    const delayMs = 1500;
    return new Promise((resolve, reject) => {
      const tryFetch = (n) => {
        listAuthFiles()
          .then((res) => {
            const files = res?.files || [];
            // Match by provider type (channel). The auth-file "type" field
            // from the server is the channel: claude, codex, xai, kimi,
            // antigravity. Skip disabled entries.
            const match = files.find((f) => {
              const ft = String(f?.type || f?.provider || '').toLowerCase();
              return ft === channel && !f?.disabled && !f?.unavailable;
            });
            if (!match) {
              if (n >= maxAttempts) {
                reject(new Error('Timed out waiting for the auth file to appear.'));
                return;
              }
              setTimeout(() => tryFetch(n + 1), delayMs);
              return;
            }
            // Auth file found — fetch the raw JSON (for tokens + all fields),
            // then fetch its models, then resolve with everything.
            const fileName = match.name || match.id || '';
            const email = match.email || '';
            const label = match.label || '';
            Promise.all([
              fetchAuthFileJSON(fileName).catch(() => null),
              getAuthFileModels(fileName).catch(() => null),
            ]).then(([rawJson, mres]) => {
              const modelList = (mres?.models || []).map((m) => {
                const model = { name: m.id || m.name || '' };
                if (m.display_name) model.display_name = m.display_name;
                return model;
              }).filter((m) => m.name);

              // Extract all available fields from the raw auth JSON so they
              // can be persisted into the upstream_providers row.
              const authData = {
                file_name: fileName,
                email: email || strVal(rawJson, 'email'),
                label,
                models: modelList,
              };
              if (rawJson) {
                // Token fields (nested "token" object OR flat top-level).
                const tokenObj = rawJson.token || {};
                authData.token_access_token =
                  strVal(rawJson, 'access_token') || strVal(tokenObj, 'access_token');
                authData.token_refresh_token =
                  strVal(rawJson, 'refresh_token') || strVal(tokenObj, 'refresh_token');
                authData.token_token_type =
                  strVal(rawJson, 'token_type') || strVal(tokenObj, 'token_type');
                authData.token_scope =
                  strVal(rawJson, 'scope') || strVal(tokenObj, 'scope');
                authData.token_expiry =
                  strVal(rawJson, 'expires_at') || strVal(rawJson, 'expire') ||
                  strVal(rawJson, 'expired') || strVal(tokenObj, 'expiry');
                if (rawJson.expired === true) authData.token_expired = true;
                // Cloak fields (Claude OAuth).
                authData.cloak_mode = strVal(rawJson, 'cloak_mode');
                if (rawJson.cloak_strict_mode === true) authData.cloak_strict_mode = true;
                if (Array.isArray(rawJson.cloak_sensitive_words)) {
                  authData.cloak_sensitive_words = rawJson.cloak_sensitive_words;
                }
                if (rawJson.cloak_cache_user_id === true || rawJson.cloak_cache_user_id === false) {
                  authData.cloak_cache_user_id = rawJson.cloak_cache_user_id;
                }
                // Pass-through extras.
                if (rawJson.disable_cooling === true) authData.disable_cooling = true;
                if (rawJson.request_retry != null) authData.request_retry = rawJson.request_retry;
                if (rawJson.tool_prefix_disabled === true) authData.tool_prefix_disabled = true;
                if (rawJson.prefix) authData.prefix = rawJson.prefix;
              }
              resolve(authData);
            });
          })
          .catch(() => {
            if (n >= maxAttempts) {
              reject(new Error('Failed to fetch auth files.'));
              return;
            }
            setTimeout(() => tryFetch(n + 1), delayMs);
          });
      };
      tryFetch(attempt);
    });
  };

  const handleCopy = () => {
    if (!authUrl) return;
    navigator.clipboard?.writeText(authUrl).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="form-section">
      <div className="form-section__title">Connect {TYPE_LABEL[providerType] || providerType}</div>
      <div className="form-section__hint">
        Start the OAuth login flow in your browser, then paste the callback URL
        you land on after authorizing. This exchanges the code for tokens so the
        provider is ready to route requests.
      </div>

      {stage === 'idle' && (
        <div className="form__row">
          <button type="button" className="primary" onClick={handleGenerate}>
            Generate authorize URL
          </button>
        </div>
      )}

      {stage === 'loading' && <Spinner label="Requesting authorize URL…" />}

      {stage === 'fetching' && <Spinner label="Fetching credential details from server…" />}

      {stage === 'error' && (
        <>
          <div className="error-banner">{error}</div>
          <button type="button" onClick={handleGenerate}>Try again</button>
        </>
      )}

      {(stage === 'ready' || stage === 'submitting' || stage === 'done') && (
        <>
          {stage === 'done' ? (
            <div className="success-banner">
              ✓ OAuth flow completed. Save the provider to persist the credential.
            </div>
          ) : (
            <div className="form__row">
              <label className="form__label">Authorize URL</label>
              <div className="copyable" style={{ wordBreak: 'break-all', fontSize: 12 }}>{authUrl}</div>
              <div className="row gap-sm" style={{ marginTop: 6 }}>
                <button type="button" onClick={() => window.open(authUrl, '_blank', 'noopener,noreferrer')}>
                  Re-open in new tab
                </button>
                <button type="button" onClick={handleCopy}>{copied ? '✓ Copied' : 'Copy URL'}</button>
              </div>
              {flow === 'device' && userCode && (
                <div className="form__hint" style={{ marginTop: 8 }}>
                  Device code: <strong>{userCode}</strong>
                </div>
              )}
              <div className="form__hint" style={{ marginTop: 8 }}>
                Complete the login in the browser tab. After authorizing, you will
                be redirected to a callback URL — copy it from the browser address
                bar and paste it below.
              </div>
            </div>
          )}

          {stage !== 'done' && (
            <div className="form__row">
              <label className="form__label">Callback URL</label>
              <input
                type="text"
                value={callbackUrl}
                onChange={(e) => setCallbackUrl(e.target.value)}
                placeholder="https://your-server/anthropic/callback?code=…&state=…"
                spellCheck={false}
              />
              <div className="form__hint">
                Paste the full redirect URL from the browser address bar after
                completing the login. The server extracts the code and state
                automatically.
              </div>
              <div className="row gap-sm" style={{ marginTop: 8 }}>
                <button
                  type="button"
                  className="primary"
                  onClick={handleSubmitCallback}
                  disabled={stage === 'submitting' || !callbackUrl.trim()}
                >
                  {stage === 'submitting' ? 'Submitting…' : 'Complete OAuth'}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
