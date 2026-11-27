import { createContext } from 'react';
import type { PluginListEntry } from '@/types';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';
import { resolvePluginAssetURL } from './pluginResources';

export const EMPTY_PROVIDER_LOGOS: ReadonlyMap<string, string> = new Map();
export const PluginProviderLogos = createContext(EMPTY_PROVIDER_LOGOS);

export function collectPluginProviderLogos(plugins: PluginListEntry[], apiBase: string) {
  const logos = new Map<string, string>();
  for (const plugin of plugins) {
    if (!plugin.effectiveEnabled) continue;
    const defaultLogo = plugin.logo.trim() || plugin.metadata?.logo || '';
    const register = (provider: string | undefined, logo: string) => {
      if (!provider || !logo.trim()) return;
      const key = normalizeOAuthProviderKey(provider);
      if (key && !logos.has(key)) logos.set(key, resolvePluginAssetURL(logo, apiBase));
    };
    for (const entry of plugin.oauthProviderEntries ?? []) {
      register(entry.identifier, entry.logo?.trim() || defaultLogo);
    }
    for (const provider of plugin.oauthProviders ?? []) register(provider, defaultLogo);
    register(plugin.oauthProvider, defaultLogo);
    register(plugin.quotaProvider, defaultLogo);
  }
  return logos;
}
