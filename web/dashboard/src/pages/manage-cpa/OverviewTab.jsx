import React, { useState, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner, Stat, EmptyState } from '../../components/Primitives.jsx';
import {
  getCpaConfig,
  getCpaLatestVersion,
  listAuthFiles,
  getProviderKeys,
  PROVIDER_KEY_KINDS,
  countAuthFilesByProvider,
  extractProviderList,
  fmtBool,
} from './hooks.js';

// OverviewTab — landing page inside /manage-cpa.
//
// Shows the running CPA server's vital signs at a glance:
//   - Server config: host/port, debug, logging, request-retry, routing,
//     proxy URL, latest GitHub release version.
//   - Provider entries: count per OAuth provider (from auth-files list) and
//     count per API-key provider (from each provider's *-api-key endpoint).
//
// We use independent useAsync calls for each data source so a slow
// provider endpoint never blocks the rest of the page. A "Reload" button
// at the top bumps all the tokens in lockstep.
export default function OverviewTab() {
  const [configToken, setConfigToken] = useState(0);
  const [authToken, setAuthToken] = useState(0);
  const [versionToken, setVersionToken] = useState(0);
  const [keyTokens, setKeyTokens] = useState({});

  const reloadAll = useCallback(() => {
    setConfigToken((t) => t + 1);
    setAuthToken((t) => t + 1);
    setVersionToken((t) => t + 1);
    setKeyTokens((m) => {
      const next = {};
      for (const k of Object.keys(m)) next[k] = m[k] + 1;
      return next;
    });
  }, []);

  const configReq = useAsync(() => getCpaConfig(), [configToken]);
  const authReq = useAsync(() => listAuthFiles(), [authToken]);
  const versionReq = useAsync(
    () => getCpaLatestVersion().catch(() => null),
    [versionToken],
  );

  const config = configReq.data;
  const authFiles = authReq.data?.files;
  const version = versionReq.data;

  return (
    <>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <div className="dim">
          Live snapshot of the running CLIProxyAPI. Counts come from a fresh
          fetch on every reload.
        </div>
        <button onClick={reloadAll}>Refresh</button>
      </div>

      {/* Server config card -------------------------------------------- */}
      <div className="card">
        <h3 className="card__title">Server config</h3>
        {configReq.loading && <Spinner label="Loading config…" />}
        <ErrorBanner error={configReq.error} />
        {!configReq.loading && !configReq.error && config && (
          <div className="grid grid--3">
            <Stat
              label="Host:port"
              value={`${config.host || '0.0.0.0'}:${config.port ?? '—'}`}
              delta={config.tls?.enable ? 'TLS enabled' : 'plain HTTP'}
            />
            <Stat
              label="Debug"
              value={fmtBool(config.debug, 'enabled', 'off')}
              delta={config.debug ? 'verbose logs' : 'quiet'}
            />
            <Stat
              label="Logging to file"
              value={fmtBool(config.logging_to_file, 'on', 'off')}
              delta={config.logging_to_file ? `${config.logs_max_total_size_mb || 0} MB cap` : 'stdout only'}
            />
            <Stat
              label="Routing strategy"
              value={config.routing?.strategy || 'round-robin'}
              delta={config.routing?.session_affinity ? 'session affinity on' : null}
            />
            <Stat
              label="Request retry"
              value={String(config.request_retry ?? '—')}
              delta={`max interval ${config.max_retry_interval ?? '—'}s`}
            />
            <Stat
              label="Proxy URL"
              value={config.proxy_url || 'none'}
              delta={config.proxy_url ? 'outbound via proxy' : 'direct'}
            />
            <Stat
              label="Force model prefix"
              value={fmtBool(config.force_model_prefix)}
            />
            <Stat
              label="Request log"
              value={fmtBool(config.request_log)}
            />
            <Stat
              label="WebSocket auth"
              value={fmtBool(config.ws_auth)}
            />
          </div>
        )}
        {!configReq.loading && !configReq.error && !config && (
          <EmptyState title="No config returned" hint="The server did not return a Config object." />
        )}
      </div>

      {/* Auth-files card ----------------------------------------------- */}
      <div className="card">
        <h3 className="card__title">Auth-files (provider accounts)</h3>
        {authReq.loading && <Spinner label="Loading auth-files…" />}
        <ErrorBanner error={authReq.error} />
        {!authReq.loading && !authReq.error && Array.isArray(authFiles) && (
          <>
            <div className="grid grid--3">
              <Stat
                label="Total auth-files"
                value={String(authFiles.length)}
                delta="across all providers"
              />
              <Stat
                label="Active"
                value={String(
                  authFiles.filter((f) => !f.disabled && f.status === 'active').length,
                )}
                delta="enabled + not in cooldown"
              />
              <Stat
                label="Disabled"
                value={String(authFiles.filter((f) => f.disabled).length)}
                delta="manually disabled"
              />
            </div>
            <div className="grid grid--4" style={{ marginTop: 16 }}>
              {PROVIDER_KEY_KINDS.map((k) => (
                <Stat
                  key={k.id}
                  label={k.label}
                  value={String(countAuthFilesByProvider(authFiles, k.id === 'openai' ? 'openai' : k.id))}
                  delta="OAuth accounts"
                />
              ))}
            </div>
          </>
        )}
      </div>

      {/* Provider-key card --------------------------------------------- */}
      <div className="card">
        <h3 className="card__title">Provider API-key lists</h3>
        <div className="dim" style={{ marginBottom: 12, fontSize: 12 }}>
          Count of entries per <code>*-api-key</code> / <code>openai-compatibility</code>{' '}
          list. The numbers come from each endpoint individually — refresh
          picks up stale entries after you edit on the AI Providers tab.
        </div>
        <div className="grid grid--4">
          {PROVIDER_KEY_KINDS.map((k) => (
            <ProviderKeyCount
              key={k.id}
              kind={k}
              token={keyTokens[k.id] || 0}
            />
          ))}
        </div>
      </div>

      {/* Version card -------------------------------------------------- */}
      <div className="card">
        <h3 className="card__title">Release info</h3>
        {versionReq.loading && <Spinner label="Checking GitHub…" />}
        {!versionReq.loading && versionReq.error && (
          <div className="dim">
            Could not reach GitHub: <span className="mono">{versionReq.error.message}</span>
          </div>
        )}
        {!versionReq.loading && !versionReq.error && version?.['latest-version'] && (
          <div className="grid grid--3">
            <Stat
              label="Latest CLIProxyAPI"
              value={version['latest-version']}
              delta="from GitHub releases"
            />
          </div>
        )}
        {!versionReq.loading && !versionReq.error && !version?.['latest-version'] && (
          <div className="dim">No version info available.</div>
        )}
      </div>

      <div className="card">
        <h3 className="card__title">Quick links</h3>
        <ul className="list-bare">
          <li>
            <Link to="/manage-cpa/providers">AI Providers</Link> — manage OAuth
            auth-files and per-provider API-key lists.
          </li>
          <li>
            <Link to="/manage-cpa/raw-config">Raw Config</Link> — view and
            edit <code>config.yaml</code> in place (validated by the server).
          </li>
        </ul>
      </div>
    </>
  );
}

// ProviderKeyCount — one Stat tile for a single provider's API-key list.
//
// Fetches the list once per `token` and renders the count, or a dash when
// the endpoint isn't supported by the running server. We never block the
// page on this — the tile shows a small spinner in the corner while
// loading.
function ProviderKeyCount({ kind, token }) {
  const { data, loading, error } = useAsync(() => getProviderKeys(kind.id), [token]);
  const list = extractProviderList(data, kind.field);
  return (
    <div className="stat">
      <div className="stat__label">{kind.label}</div>
      <div className="stat__value">
        {loading ? '…' : error ? '!' : String(list.length)}
      </div>
      <div className="stat__delta">
        {error ? (
          <span style={{ color: 'var(--warning)' }}>endpoint error</span>
        ) : (
          <code className="mono">{kind.field}</code>
        )}
      </div>
    </div>
  );
}
