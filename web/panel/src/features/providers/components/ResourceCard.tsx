import { useId, type CSSProperties, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { IconEye, IconPencil, IconTrash2 } from '@/components/ui/icons';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import { getSponsorBaseUrl, isSponsorResource, type ResourceUsage } from '../resourceView';
import type { ProviderResource } from '../types';
import styles from './ResourceCard.module.scss';
import statusBarStyles from './providerStatusBar.module.scss';

interface ResourceCardProps {
  resource: ProviderResource;
  selected: boolean;
  disableMutations: boolean;
  /** Recent request health; null for sponsor bundles and before usage loads. */
  usage: ResourceUsage | null;
  /** One-shot cascade delay on first data arrival; omitted afterwards. */
  enterDelayMs?: number;
  onView: (resource: ProviderResource) => void;
  onEdit: (resource: ProviderResource) => void;
  onDelete: (resource: ProviderResource) => void;
  onToggleDisabled: (resource: ProviderResource, disabled: boolean) => void;
}

const CLAUDE_DEFAULT_BASE_URL = 'https://api.anthropic.com';

/**
 * Credential card — identity first, then endpoint, capabilities and health.
 * Disabled cards dim their content but keep the action row crisp for re-enabling.
 */
export function ResourceCard({
  resource,
  selected,
  disableMutations,
  usage,
  enterDelayMs,
  onView,
  onEdit,
  onDelete,
  onToggleDisabled,
}: ResourceCardProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const sponsor = isSponsorResource(resource);
  const protocols = resource.flags.protocols ?? [];

  const named = Boolean(resource.name);
  const title = resource.name ?? resource.apiKeyPreview ?? resource.identifier;
  const subParts: string[] = [];
  if (sponsor) {
    subParts.push(resource.apiKeyPreview ?? t('providersPage.status.notConfigured'));
  } else if (resource.brand === 'openaiCompatibility') {
    const extra = resource.apiKeyEntryCount > 1 ? ` +${resource.apiKeyEntryCount - 1}` : '';
    subParts.push(`${resource.apiKeyPreview ?? '—'}${extra}`);
  } else {
    if (named && resource.apiKeyPreview) subParts.push(resource.apiKeyPreview);
    if (resource.authIndex) subParts.push(`auth ${resource.authIndex}`);
  }

  let endpoint: ReactNode;
  const sponsorBaseUrl = sponsor ? getSponsorBaseUrl(resource) : null;
  if (sponsorBaseUrl) {
    endpoint = sponsorBaseUrl;
  } else if (sponsor) {
    endpoint = <span className={styles.endpointNote}>{t('providersPage.status.notSet')}</span>;
  } else if (resource.baseUrl) {
    endpoint = resource.baseUrl;
  } else if (resource.brand === 'claude') {
    endpoint = (
      <>
        {CLAUDE_DEFAULT_BASE_URL}{' '}
        <span className={styles.endpointNote}>{t('providersPage.status.defaultSuffix')}</span>
      </>
    );
  } else {
    endpoint = <span className={styles.endpointNote}>{t('providersPage.status.notSet')}</span>;
  }
  const endpointTitle = sponsor
    ? (sponsorBaseUrl ?? undefined)
    : (resource.baseUrl ?? (resource.brand === 'claude' ? CLAUDE_DEFAULT_BASE_URL : undefined));

  const chips: ReactNode[] = [];
  if (resource.prefix) {
    chips.push(
      <span key="prefix" className={`${styles.chip} ${styles.chipPrefix}`}>
        <span className={styles.chipLabel}>{t('providersPage.detail.fields.prefix')}</span>
        {resource.prefix}
      </span>
    );
  }
  const metric = (key: string, label: string, value: number) => (
    <span key={key} className={`${styles.chip} ${value === 0 ? styles.chipZero : ''}`}>
      <span className={styles.chipLabel}>{label}</span>
      {value}
    </span>
  );
  const tag = (key: string, label: string) => (
    <span key={key} className={`${styles.chip} ${styles.chipTag}`}>
      {label}
    </span>
  );
  if (sponsor) {
    protocols.forEach((protocol) =>
      chips.push(tag(protocol, t(`providersPage.sponsor.protocols.${protocol}`)))
    );
  } else {
    chips.push(metric('models', t('providersPage.table.metrics.models'), resource.modelCount));
    if (resource.brand === 'openaiCompatibility') {
      chips.push(metric('keys', t('providersPage.table.metrics.keys'), resource.apiKeyEntryCount));
    }
    chips.push(metric('headers', t('providersPage.table.metrics.headers'), resource.headerCount));
    if (resource.priority !== 0) {
      chips.push(metric('priority', t('providersPage.toolbar.sort.priority'), resource.priority));
    }
    if ((resource.brand === 'codex' || resource.brand === 'xai') && resource.flags.websockets) {
      chips.push(tag('ws', t('providersPage.table.websocketsTag')));
    }
    if (resource.brand === 'claude' && resource.flags.cloakEnabled) {
      chips.push(tag('cloak', t('providersPage.table.cloakTag')));
    }
    if (resource.brand === 'claude' && resource.flags.claudeCodeCliProfile) {
      chips.push(tag('cli-profile', t('providersPage.table.cliProfileTag')));
    }
  }

  const cardClass = [
    styles.card,
    resource.disabled ? styles.cardDisabled : '',
    selected ? styles.cardSelected : '',
    enterDelayMs != null ? styles.cardEnter : '',
  ]
    .filter(Boolean)
    .join(' ');
  const cardStyle =
    enterDelayMs != null ? ({ '--card-delay': `${enterDelayMs}ms` } as CSSProperties) : undefined;
  const hasTraffic = usage ? usage.success + usage.failure > 0 : false;

  return (
    <article className={cardClass} style={cardStyle} aria-labelledby={titleId}>
      <div className={styles.body}>
        <div className={styles.head}>
          <h3 className={styles.titleWrap}>
            <button
              id={titleId}
              type="button"
              className={`${styles.title} ${named ? '' : styles.titleMono}`}
              onClick={() => onView(resource)}
            >
              {title}
            </button>
          </h3>
          {resource.disabled ? (
            <span className={styles.stateBadge}>{t('providersPage.status.disabled')}</span>
          ) : null}
        </div>
        {subParts.length > 0 ? <div className={styles.sub}>{subParts.join(' · ')}</div> : null}

        <div className={styles.endpoint} title={endpointTitle}>
          {endpoint}
        </div>

        {chips.length > 0 ? <div className={styles.chips}>{chips}</div> : null}

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
      </div>

      <footer className={styles.actions}>
        <div className={styles.utility}>
          <button
            type="button"
            className={styles.iconButton}
            aria-label={t('providersPage.actions.view')}
            title={t('providersPage.actions.view')}
            onClick={() => onView(resource)}
          >
            <IconEye size={15} />
          </button>
          <button
            type="button"
            className={styles.iconButton}
            aria-label={t('providersPage.actions.edit')}
            title={t('providersPage.actions.edit')}
            disabled={disableMutations}
            onClick={() => onEdit(resource)}
          >
            <IconPencil size={15} />
          </button>
          <button
            type="button"
            className={`${styles.iconButton} ${styles.iconButtonDanger}`}
            aria-label={t('providersPage.actions.delete')}
            title={t('providersPage.actions.delete')}
            disabled={disableMutations}
            onClick={() => onDelete(resource)}
          >
            <IconTrash2 size={15} />
          </button>
        </div>
        <div className={styles.toggle}>
          <span className={styles.toggleLabel} aria-hidden="true">
            {t('providersPage.actions.enable')}
          </span>
          <ToggleSwitch
            checked={!resource.disabled}
            disabled={disableMutations}
            onChange={(value) => onToggleDisabled(resource, !value)}
            ariaLabel={
              resource.disabled
                ? t('providersPage.actions.enable')
                : t('providersPage.actions.disable')
            }
          />
        </div>
      </footer>
    </article>
  );
}
