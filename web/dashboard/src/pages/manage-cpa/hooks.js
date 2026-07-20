// High-level fetch helpers for the "Manage CPA" pages.
//
// This module re-exports the raw `cpaFetch` wrappers from `../api/client.js`
// under names that read more naturally at the call site, plus a few small
// pure helpers for shaping the responses (e.g. counting provider entries).
//
// Keep this file thin — it is glue, not a data layer. Anything that needs
// derived state (KPI rollups, status normalization) should live in the
// component that uses it so React's render loop remains the source of
// truth.

import {
  getCpaConfig,
  getCpaConfigYaml,
  putCpaConfigYaml,
  getCpaLatestVersion,
  listAuthFiles,
  getAuthFileModels,
  patchAuthFileStatus,
  deleteAuthFile,
  getProviderKeys,
  putProviderKeys,
  patchProviderKey,
  deleteProviderKey,
  listOAuthProviders,
  requestOAuthUrl,
  downloadAuthFile,
} from '../../api/client.js';

export {
  getCpaConfig,
  getCpaConfigYaml,
  putCpaConfigYaml,
  getCpaLatestVersion,
  listAuthFiles,
  getAuthFileModels,
  patchAuthFileStatus,
  deleteAuthFile,
  getProviderKeys,
  putProviderKeys,
  patchProviderKey,
  deleteProviderKey,
  listOAuthProviders,
  requestOAuthUrl,
  downloadAuthFile,
};

// --- Derived helpers --------------------------------------------------------

// Provider key surfaces shown in the Providers tab. Order = display order.
export const PROVIDER_KEY_KINDS = [
  { id: 'gemini', label: 'Gemini', field: 'gemini-api-key' },
  { id: 'interactions', label: 'Interactions', field: 'interactions-api-key' },
  { id: 'claude', label: 'Claude', field: 'claude-api-key' },
  { id: 'codex', label: 'Codex', field: 'codex-api-key' },
  { id: 'xai', label: 'xAI', field: 'xai-api-key' },
  { id: 'vertex', label: 'Vertex', field: 'vertex-api-key' },
  { id: 'openai', label: 'OpenAI Compatibility', field: 'openai-compatibility' },
];

// Extract the array under the standard <field>-api-key / openai-compatibility
// shape that the Go handler returns. Each endpoint wraps its list in a
// single-key object so we can ignore a missing or null payload gracefully.
export function extractProviderList(payload, field) {
  if (!payload || typeof payload !== 'object') return [];
  const value = payload[field];
  return Array.isArray(value) ? value : [];
}

// Read a count of auth-files matching a provider key. Falls back to 0 when
// the field is missing (older CPA versions did not always populate it).
export function countAuthFilesByProvider(authFiles, provider) {
  if (!Array.isArray(authFiles)) return 0;
  return authFiles.filter((f) => (f?.type || f?.provider) === provider).length;
}

// Pretty-print a bool flag for KPI cards.
export function fmtBool(value, yesLabel = 'on', noLabel = 'off') {
  return value ? yesLabel : noLabel;
}

// Mask a secret string so it can be shown in tables without leaking it
// (e.g. an API key prefix view).
export function maskKey(value, head = 4, tail = 4) {
  if (!value) return '—';
  const s = String(value);
  if (s.length <= head + tail) return s;
  return `${s.slice(0, head)}…${s.slice(-tail)}`;
}

// Format an ISO timestamp / RFC3339 string for display in tables.
export function fmtTime(value) {
  if (!value) return '—';
  try {
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toLocaleString();
  } catch {
    return '—';
  }
}
