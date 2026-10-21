import type { AuthFileItem } from '@/types';
import { KIMI_AI_USAGE_URL, KIMI_USAGE_URL } from '@/utils/quota/constants';
import { authFilesApi } from './authFiles';

const domainFromValue = (value: unknown): 'ai' | 'com' | null => {
  if (typeof value !== 'string') return null;
  const domain = value.trim().toLowerCase();
  if (['ai', 'kimi-ai', 'kimi.ai'].includes(domain) || domain.endsWith('.kimi.ai')) {
    return 'ai';
  }
  if (['com', 'kimi', 'kimi.com'].includes(domain) || domain.endsWith('.kimi.com')) {
    return 'com';
  }
  return null;
};

const domainFromBaseUrl = (value: unknown): 'ai' | 'com' | null => {
  if (typeof value !== 'string') return null;
  try {
    const host = new URL(value.trim()).hostname.toLowerCase();
    if (host === 'kimi.ai' || host.endsWith('.kimi.ai')) return 'ai';
    if (host === 'kimi.com' || host.endsWith('.kimi.com')) return 'com';
  } catch {
    // Unrecognized base URLs do not override the credential type in the backend.
  }
  return null;
};

/** Match the backend's file synthesizer: explicit domain, base_url, then type/provider. */
export function parseKimiQuotaUrl(text: string, file: AuthFileItem): string {
  // Do not propagate JSON parser errors: they may contain credential contents.
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Error('Invalid Kimi credential');
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Invalid Kimi credential');
  }
  const credential = value as Record<string, unknown>;
  // The synthesizer normalizes a non-empty explicit domain to kimi.ai or kimi.com.
  const explicitDomain =
    typeof credential.domain === 'string' && credential.domain.trim()
      ? (domainFromValue(credential.domain) ?? 'com')
      : null;
  // Downloads retain raw keys; NormalizeCredentialMetadata gives an explicitly
  // present canonical key precedence, even when its value is null or empty.
  const baseUrl = Object.prototype.hasOwnProperty.call(credential, 'base_url')
    ? credential.base_url
    : credential['base-url'];
  const provider = String(file.provider ?? file.type ?? '')
    .trim()
    .replace(/_/g, '-');
  const domain =
    explicitDomain ??
    domainFromBaseUrl(baseUrl) ??
    domainFromValue(credential.type) ??
    domainFromValue(provider) ??
    (/kimi-ai|kimi\.ai/i.test(`${file.id ?? ''} ${file.name}`) ? 'ai' : 'com');
  // Never use the downloaded URL directly or expose tokens to arbitrary hosts.
  return domain === 'ai' ? KIMI_AI_USAGE_URL : KIMI_USAGE_URL;
}

/** Read only for this request; never retain the downloaded credential in a cache/store. */
export async function resolveKimiQuotaUrl(file: AuthFileItem): Promise<string> {
  if (
    !file.name?.trim() ||
    [file.runtimeOnly, file.runtime_only].some((value) => value === true || value === 'true')
  ) {
    throw new Error('Kimi credential is not downloadable');
  }
  return parseKimiQuotaUrl(await authFilesApi.downloadText(file.name), file);
}
