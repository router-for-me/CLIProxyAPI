import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Skeleton } from '@/components/ui/Skeleton';
import { IconCheck, IconExternalLink, IconGithub, IconSettings } from '@/components/ui/icons';
import type { PluginStoreEntry } from '@/types';
import {
  buildRepositoryURL,
  isDefaultPluginStoreSource,
  isOfficialPlugin,
} from '../pluginResources';
import {
  formatInstallType,
  formatPluginVersion,
  getStoreEntryKey,
  getStoreEntryTitle,
} from '../pluginStoreEntries';
import { getStoreEntryDisplay } from '../pluginStoreMetadata';
import type { StoreLayout } from '../pluginsView';
import { PillButton, PluginLogo, PluginVersionChange } from './PluginRow';
import rowStyles from './PluginRow.module.scss';
import styles from './StorePluginRow.module.scss';

// Cards have the room for one more line before the description needs a toggle.
const DESCRIPTION_LINES: Record<StoreLayout, number> = { list: 2, cards: 3 };

const getDescriptionDOMID = (entryKey: string) =>
  `plugin-store-desc-${encodeURIComponent(entryKey)}`;

/**
 * Whether a clamped paragraph hides text. Re-measured whenever the element resizes, which also
 * covers the first show of a view that was mounted hidden.
 */
function useClampOverflow(text: string, lines: number) {
  const ref = useRef<HTMLParagraphElement | null>(null);
  const [overflowing, setOverflowing] = useState(false);

  useEffect(() => {
    const node = ref.current;
    if (!node || !text) return;

    const measure = () => {
      const lineHeight = Number.parseFloat(window.getComputedStyle(node).lineHeight);
      const next =
        Number.isFinite(lineHeight) && lineHeight > 0
          ? node.scrollHeight > lineHeight * lines + 1
          : node.scrollHeight > node.clientHeight + 1;
      setOverflowing((current) => (current === next ? current : next));
    };

    if (typeof ResizeObserver === 'undefined') {
      const frame = window.requestAnimationFrame(measure);
      window.addEventListener('resize', measure);
      return () => {
        window.cancelAnimationFrame(frame);
        window.removeEventListener('resize', measure);
      };
    }

    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [text, lines]);

  return { ref, overflowing };
}

export type StoreRowAction =
  | { kind: 'install' }
  | { kind: 'update' }
  | { kind: 'installed'; onConfigure: () => void; configureDisabled: boolean };

interface StorePluginRowProps {
  entry: PluginStoreEntry;
  layout: StoreLayout;
  logo: string;
  action: StoreRowAction;
  installing: boolean;
  /** Install/Update cannot start (not connected, or another install is running). */
  disabled: boolean;
  onInstall: () => void;
}

export function StorePluginRow({
  entry,
  layout,
  logo,
  action,
  installing,
  disabled,
  onInstall,
}: StorePluginRowProps) {
  const { t, i18n } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const entryKey = getStoreEntryKey(entry);
  const display = getStoreEntryDisplay(entry, i18n.language);
  const title = getStoreEntryTitle(entry, i18n.language);
  const { ref: descriptionRef, overflowing } = useClampOverflow(
    display.description,
    DESCRIPTION_LINES[layout]
  );
  const descriptionID = getDescriptionDOMID(entryKey);

  const repositoryURL = buildRepositoryURL(entry.repository);
  const homepageURL = /^https?:\/\//i.test(display.homepage) ? display.homepage : '';
  const missingAuth = entry.authRequired && !entry.authConfigured;
  const sourceName = isDefaultPluginStoreSource(entry)
    ? t('plugin_store.cli_proxy_api_source')
    : entry.sourceName;
  const installedVersion = entry.installed ? entry.installedVersion : '';
  const listedVersion = installedVersion || entry.version;

  const metaItems = [
    display.author,
    sourceName ? t('plugin_store.source_name', { source: sourceName }) : '',
    display.license,
    entry.installType
      ? t('plugin_store.install_type', { type: formatInstallType(entry.installType) })
      : '',
    entry.platforms.length > 0
      ? t('plugin_store.platforms', {
          platforms: entry.platforms
            .map((platform) => `${platform.goos}/${platform.goarch}`)
            .join(', '),
        })
      : '',
  ].filter(Boolean);

  const repositoryLabel = t('plugin_store.open_repository');
  const homepageLabel = t('plugin_store.open_homepage');
  const configureLabel = t('plugin_management.edit_config');

  return (
    <article
      className={`${rowStyles.row} ${styles.row} ${layout === 'cards' ? styles.card : ''}`}
      data-reveal
    >
      <div className={rowStyles.logo} aria-hidden="true">
        <PluginLogo key={logo} src={logo} />
      </div>

      <div className={`${rowStyles.text} ${styles.text}`}>
        <div className={rowStyles.titleLine}>
          <h2 className={rowStyles.name}>{title}</h2>
          {action.kind === 'update' && entry.installedVersion && entry.version ? (
            <PluginVersionChange from={entry.installedVersion} to={entry.version} />
          ) : listedVersion ? (
            <span className={rowStyles.version}>{formatPluginVersion(listedVersion)}</span>
          ) : null}
          {!isOfficialPlugin(entry) ? (
            <span className={`${rowStyles.tag} ${rowStyles.tagCaution}`}>
              {t('plugin_store.badge_untrusted')}
            </span>
          ) : null}
        </div>

        {display.description ? (
          <div className={styles.descriptionBlock}>
            <p
              id={descriptionID}
              ref={descriptionRef}
              className={`${styles.description} ${expanded ? styles.descriptionExpanded : ''}`}
            >
              {display.description}
            </p>
            {overflowing ? (
              <button
                type="button"
                className={styles.descriptionToggle}
                onClick={() => setExpanded((current) => !current)}
                aria-expanded={expanded}
                aria-controls={descriptionID}
              >
                {t(
                  expanded
                    ? 'plugin_store.description_show_less'
                    : 'plugin_store.description_show_more'
                )}
              </button>
            ) : null}
          </div>
        ) : null}

        {metaItems.length > 0 || entry.authRequired || display.tags.length > 0 ? (
          <div className={styles.meta}>
            <div className={styles.metaInner}>
              {metaItems.map((item, index) => (
                <span key={`meta-${index}`} className={styles.metaItem}>
                  {item}
                </span>
              ))}
              {entry.authRequired ? (
                <span
                  className={`${styles.metaItem} ${missingAuth ? styles.metaWarning : ''}`}
                  title={missingAuth ? t('plugin_store.auth_required_hint') : undefined}
                >
                  {missingAuth
                    ? t('plugin_store.auth_required')
                    : t('plugin_store.auth_configured')}
                </span>
              ) : null}
              {display.tags.length > 0 ? (
                <span className={styles.tags}>
                  {display.tags.map((tag) => (
                    <span key={tag} className={styles.tagItem}>
                      {tag}
                    </span>
                  ))}
                </span>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>

      <div className={`${rowStyles.actions} ${styles.actions}`}>
        <span className={styles.links}>
          {repositoryURL ? (
            <a
              className={rowStyles.iconButton}
              href={repositoryURL}
              target="_blank"
              rel="noreferrer"
              title={repositoryLabel}
              aria-label={repositoryLabel}
            >
              <IconGithub size={16} />
            </a>
          ) : null}
          {homepageURL ? (
            <a
              className={rowStyles.iconButton}
              href={homepageURL}
              target="_blank"
              rel="noreferrer"
              title={homepageLabel}
              aria-label={homepageLabel}
            >
              <IconExternalLink size={16} />
            </a>
          ) : null}
          {action.kind === 'installed' ? (
            <button
              type="button"
              className={rowStyles.iconButton}
              onClick={action.onConfigure}
              disabled={action.configureDisabled}
              title={configureLabel}
              aria-label={configureLabel}
            >
              <IconSettings size={16} />
            </button>
          ) : null}
        </span>
        {action.kind === 'installed' ? (
          <span className={styles.installedState}>
            <IconCheck size={13} />
            {t('plugin_store.badge_installed')}
          </span>
        ) : (
          <PillButton
            label={action.kind === 'update' ? t('plugin_store.update') : t('plugin_store.install')}
            prominent={action.kind === 'update'}
            loading={installing}
            disabled={disabled || missingAuth}
            title={missingAuth ? t('plugin_store.auth_required_hint') : undefined}
            onClick={onInstall}
          />
        )}
      </div>
    </article>
  );
}

export function StorePluginRowSkeleton({ layout }: { layout: StoreLayout }) {
  return (
    <div
      className={`${rowStyles.row} ${styles.row} ${layout === 'cards' ? styles.card : ''}`}
      aria-hidden="true"
    >
      <Skeleton width={40} height={40} rounded={10} />
      <div className={`${rowStyles.text} ${styles.skeletonText}`}>
        <Skeleton width="34%" height={14} rounded={4} />
        <Skeleton width="78%" height={12} rounded={4} />
        <Skeleton width="52%" height={11} rounded={4} />
      </div>
      <div className={`${rowStyles.actions} ${styles.actions}`}>
        <span className={styles.links}>
          <Skeleton width={32} height={32} rounded={8} />
        </span>
        <span className={styles.skeletonPill}>
          <Skeleton width={80} height={28} rounded={999} />
        </span>
      </div>
    </div>
  );
}
