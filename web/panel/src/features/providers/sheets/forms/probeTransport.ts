import { getConfigValue } from '@/services/api/configValue';

/** An unchanged blank delegates transport to api-call. A cleared credential proxy must
 * skip that saved credential and use the server's global transport instead.
 */
export async function resolveProbeTransport(
  proxy: string | undefined,
  savedProxy: string | undefined,
  index: string | undefined,
  needsToken: boolean
) {
  const authIndex = index?.trim() || undefined;
  const proxyUrl = proxy?.trim() || undefined;
  if (proxy === undefined || proxyUrl || !authIndex || !savedProxy?.trim()) {
    return { authIndex, proxyUrl };
  }
  if (!needsToken) return { authIndex: undefined, proxyUrl: undefined };

  // $TOKEN$ requires authIndex, so omission would select the saved proxy. Only in this
  // case do we need a fresh global setting; never guess direct on a failed read.
  const globalProxy = await getConfigValue<string>('/config/requests/proxy-url', '');
  return { authIndex, proxyUrl: globalProxy.trim() || 'direct' };
}
