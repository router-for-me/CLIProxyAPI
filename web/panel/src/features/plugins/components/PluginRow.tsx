import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconGithub, IconPlug, IconSettings, IconTrash2 } from '@/components/ui/icons';
import type { PluginListEntry } from '@/types';
import { getPluginTitle } from '../pluginResources';
import { formatPluginVersion } from '../pluginStoreEntries';
import { PLUGIN_STATUS_LABEL_KEY, PLUGIN_STATUS_TONE, type PluginRowStatus } from '../pluginStatus';
import styles from './PluginRow.module.scss';

const TONE_CLASS = {
  success: styles.toneSuccess,
  muted: styles.toneMuted,
  warning: styles.toneWarning,
  pending: styles.tonePending,
} as const;

export function PluginLogo({ src, size = 18 }: { src: string; size?: number }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlug size={size} />
  );
}

export function PluginStatusLabel({ status }: { status: PluginRowStatus }) {
  const { t } = useTranslation();
  return (
    <span className={`${styles.status} ${TONE_CLASS[PLUGIN_STATUS_TONE[status]]}`}>
      <span className={styles.statusDot} aria-hidden="true" />
      {t(PLUGIN_STATUS_LABEL_KEY[status])}
    </span>
  );
}

/** `current → next`; the arrow is decorative, the accessible text reads both versions. */
export function PluginVersionChange({ from, to }: { from: string; to: string }) {
  return (
    <span className={styles.versionChange}>
      <span className={styles.version}>{formatPluginVersion(from)}</span>
      <span className={styles.versionArrow} aria-hidden="true">
        →
      </span>
      <span className={styles.versionNext}>{formatPluginVersion(to)}</span>
    </span>
  );
}

interface PillButtonProps {
  label: string;
  prominent?: boolean;
  loading?: boolean;
  disabled?: boolean;
  title?: string;
  onClick: () => void;
}

/** The row's one text action (Install / Update), sized so it lines up across rows. */
export function PillButton({
  label,
  prominent,
  loading,
  disabled,
  title,
  onClick,
}: PillButtonProps) {
  return (
    <button
      type="button"
      className={`${styles.pill} ${prominent ? styles.pillProminent : ''}`}
      onClick={onClick}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      title={title}
    >
      {loading ? (
        <span className={`loading-spinner ${styles.pillSpinner}`} aria-hidden="true" />
      ) : null}
      <span>{label}</span>
    </button>
  );
}

export interface PluginRowUpdate {
  from: string;
  to: string;
  /** Why the update cannot start (missing store auth), shown as the button's tooltip. */
  blockedReason: string;
  installing: boolean;
  disabled: boolean;
  onUpdate: () => void;
}

interface PluginRowProps {
  plugin: PluginListEntry;
  logo: string;
  status: PluginRowStatus;
  checked: boolean;
  disabled: boolean;
  update: PluginRowUpdate | null;
  onToggle: (enabled: boolean) => void;
  onConfigure: () => void;
  onDelete: () => void;
}

export function PluginRow({
  plugin,
  logo,
  status,
  checked,
  disabled,
  update,
  onToggle,
  onConfigure,
  onDelete,
}: PluginRowProps) {
  const { t } = useTranslation();
  const title = getPluginTitle(plugin);
  const version = plugin.metadata?.version.trim();
  const author = plugin.metadata?.author.trim();
  const github = plugin.metadata?.githubRepository.trim();
  const configureLabel = t('plugin_management.edit_config');
  const repositoryLabel = t('plugin_management.open_repository');
  const deleteLabel = t('plugin_management.delete_plugin');

  return (
    <article className={styles.row} data-reveal>
      <div className={styles.logo} aria-hidden="true">
        <PluginLogo key={logo} src={logo} />
      </div>

      <div className={styles.text}>
        <div className={styles.titleLine}>
          <h2 className={styles.name}>{title}</h2>
          {update ? (
            <PluginVersionChange from={update.from || version || ''} to={update.to} />
          ) : version ? (
            <span className={styles.version}>{formatPluginVersion(version)}</span>
          ) : null}
          {plugin.supportsOAuth ? (
            <span className={styles.tag}>{t('plugin_management.oauth')}</span>
          ) : null}
        </div>
        <div className={styles.metaLine}>
          <PluginStatusLabel status={status} />
          {title !== plugin.id ? (
            <span className={styles.id} title={plugin.id}>
              {plugin.id}
            </span>
          ) : null}
          {author ? (
            <span className={styles.author} title={author}>
              {author}
            </span>
          ) : null}
        </div>
      </div>

      <div className={styles.actions}>
        {update ? (
          <span className={styles.updateSlot}>
            <PillButton
              label={t('plugin_store.update')}
              prominent
              loading={update.installing}
              disabled={update.disabled}
              title={update.blockedReason || undefined}
              onClick={update.onUpdate}
            />
          </span>
        ) : null}
        <span className={styles.iconGroup}>
          <button
            type="button"
            className={styles.iconButton}
            onClick={onConfigure}
            disabled={disabled}
            title={configureLabel}
            aria-label={configureLabel}
          >
            <IconSettings size={16} />
          </button>
          {github ? (
            <a
              className={styles.iconButton}
              href={github}
              target="_blank"
              rel="noreferrer"
              title={repositoryLabel}
              aria-label={repositoryLabel}
            >
              <IconGithub size={16} />
            </a>
          ) : null}
          <button
            type="button"
            className={`${styles.iconButton} ${styles.danger}`}
            onClick={onDelete}
            disabled={disabled}
            title={deleteLabel}
            aria-label={deleteLabel}
          >
            <IconTrash2 size={16} />
          </button>
        </span>
        <span className={styles.toggle}>
          <ToggleSwitch
            checked={checked}
            onChange={onToggle}
            disabled={disabled}
            ariaLabel={t('plugin_management.enabled')}
          />
        </span>
      </div>
    </article>
  );
}

export function PluginRowSkeleton() {
  return (
    <div className={styles.row} aria-hidden="true">
      <Skeleton width={40} height={40} rounded={10} />
      <div className={styles.text}>
        <Skeleton width="38%" height={14} rounded={4} />
        <Skeleton width="56%" height={12} rounded={4} />
      </div>
      <div className={styles.actions}>
        <Skeleton width={32} height={32} rounded={8} />
        <Skeleton width={32} height={32} rounded={8} />
        <span className={styles.toggle}>
          <Skeleton width={44} height={24} rounded={999} />
        </span>
      </div>
    </div>
  );
}
