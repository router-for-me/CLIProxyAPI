import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { IconCheck, IconX } from '@/components/ui/icons';
import { InfoTip } from '@/components/ui/Tooltip';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import { getProviderTotalStats, type ProviderRecentUsageMap } from '@/components/providers/utils';
import type { OpenAIProviderConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import { resolveResourceUsage } from '../resourceView';
import {
  getSponsorProviderDefinition,
  isMultiProtocolSponsorBrand,
  sponsorProtocolI18nKey,
  sponsorProtocolUrl,
} from '../sponsorDefinitions';
import type { ProviderResource, SponsorProviderRaw } from '../types';
import statusBarStyles from '../components/providerStatusBar.module.scss';
import styles from './ResourceDetailView.module.scss';

interface ResourceDetailViewProps {
  resource: ProviderResource;
  usageByProvider?: ProviderRecentUsageMap;
}

const sponsorProtocolEntryKey = (protocol: string): string => {
  if (protocol === 'claude') return 'anthropicEntries';
  if (protocol === 'codex') return 'codexEntries';
  return `${protocol}Entries`;
};

type DetailRow = [key: string, value: ReactNode, mono?: boolean];

function DetailList({ rows }: { rows: DetailRow[] }) {
  const { t } = useTranslation();
  return (
    <dl className={styles.list}>
      {rows.map(([key, value, mono]) => (
        <div key={key} className={styles.row}>
          <dt className={styles.term}>{t(`providersPage.detail.fields.${key}`)}</dt>
          <dd className={`${styles.value} ${mono ? styles.valueMono : ''}`}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h4 className={styles.sectionHeading}>{title}</h4>
      {children}
    </section>
  );
}

/** Read-only spec sheet for one resource: identity, health, configuration, metadata. */
export function ResourceDetailView({ resource, usageByProvider }: ResourceDetailViewProps) {
  const { t } = useTranslation();
  const muted = (text: string) => <span className={styles.valueMuted}>{text}</span>;

  if (isMultiProtocolSponsorBrand(resource.brand)) {
    const definition = getSponsorProviderDefinition(resource.brand);
    const raw = resource.raw as SponsorProviderRaw;
    const openaiKeyCount = raw.openai.reduce(
      (count, item) => count + (item.config.apiKeyEntries?.length ?? 0),
      0
    );
    const firstKey =
      raw.openai
        .flatMap((item) => item.config.apiKeyEntries ?? [])
        .find((entry) => entry.apiKey?.trim())?.apiKey ??
      raw.codex.find((item) => item.config.apiKey?.trim())?.config.apiKey ??
      raw.claude.find((item) => item.config.apiKey?.trim())?.config.apiKey ??
      raw.gemini.find((item) => item.config.apiKey?.trim())?.config.apiKey;
    const baseUrl = definition.resolveBaseUrl(
      raw.openai[0]?.config.baseUrl ??
        raw.codex[0]?.config.baseUrl ??
        raw.claude[0]?.config.baseUrl ??
        raw.gemini[0]?.config.baseUrl
    );
    const protocolUrls = definition.getProtocolUrls(baseUrl);
    const protocolCounts: Record<string, number> = {
      openai: openaiKeyCount,
      codex: raw.codex.length,
      claude: raw.claude.length,
      gemini: raw.gemini.length,
    };
    const name = resource.name ?? resource.identifier;

    return (
      <div className={styles.detail}>
        <header className={styles.hero}>
          <div className={styles.heroLine}>
            <h3 className={styles.heroTitle}>{name}</h3>
            <InfoTip
              content={t('providersPage.sponsor.detailHint', { provider: definition.displayName })}
              label={name}
            />
            <StatePill disabled={resource.disabled} />
          </div>
        </header>

        <div className={styles.endpoints}>
          {definition.protocols.map((protocol) => (
            <div key={protocol} className={styles.endpoint}>
              <span className={styles.endpointName}>
                {t(`providersPage.sponsor.protocols.${sponsorProtocolI18nKey(protocol)}`)}
              </span>
              <span className={styles.endpointUrl}>
                {sponsorProtocolUrl(protocolUrls, protocol)}
              </span>
            </div>
          ))}
        </div>

        <DetailList
          rows={[
            ['identifier', firstKey ? maskApiKey(firstKey) : resource.identifier, true],
            ['prefix', resource.prefix ?? muted(t('providersPage.status.none')), true],
          ]}
        />
        <dl className={styles.list}>
          {definition.protocols.map((protocol) => (
            <div key={protocol} className={styles.row}>
              <dt className={styles.term}>
                {t(`providersPage.sponsor.${sponsorProtocolEntryKey(protocol)}`)}
              </dt>
              <dd className={`${styles.value} ${styles.valueMono}`}>{protocolCounts[protocol]}</dd>
            </div>
          ))}
        </dl>
      </div>
    );
  }

  const usage = usageByProvider ? resolveResourceUsage(resource, usageByProvider) : null;
  const openaiConfig =
    resource.brand === 'openaiCompatibility' ? (resource.raw as OpenAIProviderConfig) : null;
  const apiKeyEntries = openaiConfig?.apiKeyEntries ?? [];
  const title = resource.name ?? resource.identifier;
  const hasTraffic = usage ? usage.success + usage.failure > 0 : false;

  return (
    <div className={styles.detail}>
      <header className={styles.hero}>
        <div className={styles.heroLine}>
          <h3 className={`${styles.heroTitle} ${resource.name ? '' : styles.heroTitleMono}`}>
            {title}
          </h3>
          <StatePill disabled={resource.disabled} />
        </div>
      </header>

      {usage ? (
        <div className={styles.health}>
          <div className={styles.healthHead}>
            <span className={styles.healthLabel}>{t('providersPage.card.requests')}</span>
            <span className={styles.healthCounts}>
              <span className={hasTraffic ? styles.countOk : undefined}>
                {t('stats.success')} {usage.success}
              </span>
              <span className={usage.failure > 0 ? styles.countFail : undefined}>
                {t('stats.failure')} {usage.failure}
              </span>
            </span>
          </div>
          <ProviderStatusBar statusData={usage.statusData} styles={statusBarStyles} />
        </div>
      ) : null}

      <DetailSection title={t('providersPage.form.sections.connection')}>
        <DetailList
          rows={[
            ['identifier', resource.identifier, true],
            ['baseUrl', resource.baseUrl ?? muted(t('providersPage.status.notSet')), true],
            ['proxyUrl', resource.proxyUrl ?? muted(t('providersPage.status.notSet')), true],
            ['prefix', resource.prefix ?? muted(t('providersPage.status.none')), true],
            ['models', String(resource.modelCount), true],
            ['headers', String(resource.headerCount), true],
          ]}
        />
      </DetailSection>

      {openaiConfig && apiKeyEntries.length > 0 ? (
        <DetailSection
          title={`${t('providersPage.form.apiKeyEntriesSection')} · ${apiKeyEntries.length}`}
        >
          <ol className={styles.keys}>
            {apiKeyEntries.map((entry, entryIndex) => {
              const entryStats = usageByProvider
                ? getProviderTotalStats(
                    usageByProvider,
                    openaiConfig.name,
                    entry.apiKey,
                    openaiConfig.baseUrl
                  )
                : { success: 0, failure: 0 };
              return (
                <li key={`${entry.apiKey}-${entryIndex}`} className={styles.key}>
                  <span className={styles.keyIndex}>{entryIndex + 1}</span>
                  <span className={styles.keyMain}>
                    <span className={styles.keyValue}>{maskApiKey(entry.apiKey)}</span>
                    {entry.proxyUrl ? (
                      <span className={styles.keyProxy}>{entry.proxyUrl}</span>
                    ) : null}
                  </span>
                  <span className={styles.keyStats}>
                    <span className={entryStats.success > 0 ? styles.countOk : undefined}>
                      <IconCheck size={12} aria-hidden="true" /> {entryStats.success}
                    </span>
                    <span className={entryStats.failure > 0 ? styles.countFail : undefined}>
                      <IconX size={12} aria-hidden="true" /> {entryStats.failure}
                    </span>
                  </span>
                </li>
              );
            })}
          </ol>
        </DetailSection>
      ) : null}

      <DetailSection title={t('providersPage.detail.metadataTitle')}>
        <DetailList
          rows={[
            ['authIndex', resource.authIndex ?? muted(t('providersPage.status.notSet')), true],
            ['excludedModels', String(resource.excludedModelCount), true],
            ['apiKeyEntries', String(resource.apiKeyEntryCount), true],
          ]}
        />
      </DetailSection>
    </div>
  );
}

function StatePill({ disabled }: { disabled: boolean }) {
  const { t } = useTranslation();
  return (
    <span className={`${styles.statePill} ${disabled ? styles.statePillOff : styles.statePillOn}`}>
      {disabled ? t('providersPage.status.disabled') : t('providersPage.status.active')}
    </span>
  );
}
