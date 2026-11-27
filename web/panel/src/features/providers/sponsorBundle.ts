import type { TFunction } from 'i18next';
import { getSponsorProviderDefinition, sponsorProtocolI18nKey } from './sponsorDefinitions';
import type { Config, ProviderKeyConfig } from '@/types';
import type { SponsorProtocol, SponsorProviderBrand, SponsorProviderRaw } from './types';
import { buildApiKeyFunRaw } from './sponsor';
import { buildAiCodeMirrorRaw } from './aiCodeMirror';
import { buildQiniuCloudRaw } from './qiniuCloud';
import { buildFennoAIRaw } from './fennoAI';
import { buildKimiRaw } from './kimi';

const candidates = {
  apikeyFun: buildApiKeyFunRaw,
  aiCodeMirror: buildAiCodeMirrorRaw,
  qiniuCloud: buildQiniuCloudRaw,
  fennoAI: buildFennoAIRaw,
  kimi: buildKimiRaw,
};

// A partner form owns at most one credential per protocol. A native group is indivisible:
// never claim one member while the native editor owns its siblings (including opaque keys).
function singletonGroup(config: Pick<ProviderKeyConfig, 'source'>) {
  const keys = config.source?.group.keys;
  return !config.source || (Array.isArray(keys) && keys.length === 1);
}

export function resolveSponsorBundle(brand: SponsorProviderBrand, config?: Config | null) {
  const raw = candidates[brand](config);
  const eligible = ({ config }: { config: ProviderKeyConfig }) =>
    !!config.apiKey.trim() && singletonGroup(config);
  const openai = raw.openai.filter(
    ({ config }) =>
      singletonGroup(config) &&
      config.apiKeyEntries?.length === 1 &&
      config.apiKeyEntries[0].apiKey.trim()
  );
  const codex = raw.codex.filter(eligible);
  const claude = raw.claude.filter(eligible);
  const gemini = raw.gemini.filter(eligible);
  const ambiguousProtocols: SponsorProtocol[] = [];
  function claim<T>(protocol: SponsorProtocol, rows: T[]) {
    if (rows.length > 1) ambiguousProtocols.push(protocol);
    return rows.length === 1 ? rows : [];
  }
  const bundle: SponsorProviderRaw = {
    openai: claim('openai', openai),
    codex: claim('codex', codex),
    claude: claim('claude', claude),
    gemini: claim('gemini', gemini),
  };
  return { bundle, ambiguousProtocols };
}

export const buildSponsorBundle = (brand: SponsorProviderBrand, config?: Config | null) =>
  resolveSponsorBundle(brand, config).bundle;

export function allSponsorProtocolsBlocked(
  brand: SponsorProviderBrand,
  ambiguous?: SponsorProtocol[]
) {
  return getSponsorProviderDefinition(brand).protocols.every((protocol) =>
    ambiguous?.includes(protocol)
  );
}

export function sponsorConflictMessage(t: TFunction, protocols: SponsorProtocol[]) {
  return t('providersPage.sponsor.ambiguousProtocols', {
    protocols: protocols
      .map((protocol) => t(`providersPage.sponsor.protocols.${sponsorProtocolI18nKey(protocol)}`))
      .join(', '),
  });
}

// Preflight the whole submission before the first protocol write, not just the create branch.
export function prepareSponsorWrite(
  brand: SponsorProviderBrand,
  config: Config | null,
  entries: readonly { protocol: SponsorProtocol }[],
  t: TFunction
) {
  const { bundle, ambiguousProtocols } = resolveSponsorBundle(brand, config);
  const conflicts = ambiguousProtocols.filter((protocol) =>
    entries.some((entry) => entry.protocol === protocol)
  );
  if (conflicts.length) throw new Error(sponsorConflictMessage(t, conflicts));
  return bundle;
}

export const hasApiKeyFunConfig = (config: Config | null | undefined): boolean => {
  const raw = buildSponsorBundle('apikeyFun', config);
  return raw.openai.length > 0 || raw.claude.length > 0 || raw.codex.length > 0;
};
