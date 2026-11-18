import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { IconExternalLink } from '@/components/ui/icons';
import { getErrorMessage } from '@/utils/helpers';
import type { PluginStoreEntry } from '@/types';
import { isOfficialPlugin } from '../pluginResources';
import {
  buildGitHubReleasesPageURL,
  fetchPluginReleaseVersions,
  isValidManualReleaseTag,
  supportsPluginVersionSelection,
  type PluginReleaseVersion,
} from '../pluginReleaseVersions';
import {
  formatPluginVersion,
  getStoreEntryKey,
  getStoreEntryTitle,
  pluginVersionMatches,
} from '../pluginStoreEntries';
import styles from './PluginInstallOptionsModal.module.scss';

type InstallVersionMode = 'latest' | 'release' | 'manual';

// Module-level so reopening the dialog for the same plugin does not refetch releases.
const releaseVersionsCache = new Map<string, PluginReleaseVersion[]>();

const formatReleaseDate = (value: string, locale: string) => {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date);
};

export interface PluginInstallOptionsModalProps {
  entry: PluginStoreEntry | null;
  isUpdate: boolean;
  version: string;
  installing: boolean;
  onVersionChange: (version: string) => void;
  onClose: () => void;
  onConfirm: () => void | Promise<void>;
}

export function PluginInstallOptionsModal({
  entry,
  isUpdate,
  version,
  installing,
  onVersionChange,
  onClose,
  onConfirm,
}: PluginInstallOptionsModalProps) {
  const { i18n, t } = useTranslation();
  const [versionMode, setVersionMode] = useState<InstallVersionMode>('latest');
  const [showPrerelease, setShowPrerelease] = useState(false);
  const [releaseVersions, setReleaseVersions] = useState<PluginReleaseVersion[]>([]);
  const [releaseLoading, setReleaseLoading] = useState(false);
  const [releaseError, setReleaseError] = useState('');
  const [loadedReleaseKey, setLoadedReleaseKey] = useState('');

  const entryKey = entry ? getStoreEntryKey(entry) : '';
  const entryRepository = entry?.repository ?? '';
  const supportsVersionSelection = entry
    ? supportsPluginVersionSelection(entry.installType)
    : false;
  const releasePageURL =
    entry && supportsVersionSelection ? buildGitHubReleasesPageURL(entry.repository) : '';
  const releaseCacheKey = entryKey
    ? `${entryKey}|${entryRepository.trim()}|${entry?.installType.trim().toLowerCase() ?? ''}`
    : '';

  if (releaseCacheKey !== loadedReleaseKey) {
    const cached = releaseCacheKey ? releaseVersionsCache.get(releaseCacheKey) : undefined;
    setLoadedReleaseKey(releaseCacheKey);
    setVersionMode('latest');
    setShowPrerelease(false);
    setReleaseVersions(cached ?? []);
    setReleaseError(
      entryKey && supportsVersionSelection && !releasePageURL
        ? t('plugin_store.install_version_non_github')
        : ''
    );
    setReleaseLoading(Boolean(entryKey && supportsVersionSelection && releasePageURL && !cached));
  }

  useEffect(() => {
    if (!entryKey || !supportsVersionSelection || !releasePageURL) return;

    if (releaseVersionsCache.has(releaseCacheKey)) return;

    let active = true;

    fetchPluginReleaseVersions(entryRepository)
      .then((releases) => {
        if (!active) return;
        releaseVersionsCache.set(releaseCacheKey, releases);
        setReleaseVersions(releases);
      })
      .catch((err: unknown) => {
        if (!active) return;
        setReleaseError(getErrorMessage(err, t('plugin_store.install_versions_load_failed')));
      })
      .finally(() => {
        if (!active) return;
        setReleaseLoading(false);
      });

    return () => {
      active = false;
    };
  }, [entryKey, entryRepository, releaseCacheKey, releasePageURL, supportsVersionSelection, t]);

  const stableReleaseVersions = useMemo(
    () => releaseVersions.filter((release) => !release.prerelease),
    [releaseVersions]
  );
  const visibleReleaseVersions = useMemo(
    () =>
      showPrerelease ? releaseVersions : releaseVersions.filter((release) => !release.prerelease),
    [releaseVersions, showPrerelease]
  );
  const latestRelease = stableReleaseVersions[0] ?? releaseVersions[0] ?? null;
  const releaseOptions = useMemo(
    () =>
      visibleReleaseVersions.map((release) => {
        const releaseTitle =
          release.name && release.name !== release.tagName
            ? `${release.tagName} - ${release.name}`
            : release.tagName;
        const releaseDate = formatReleaseDate(release.publishedAt, i18n.language);
        const labelParts = [
          releaseTitle,
          releaseDate,
          release.prerelease ? t('plugin_store.install_version_prerelease_badge') : '',
          release.assetNames.length > 0
            ? t('plugin_store.install_version_assets_count', {
                count: release.assetNames.length,
              })
            : '',
        ].filter(Boolean);
        return {
          value: release.tagName,
          label: labelParts.join(' · '),
        };
      }),
    [i18n.language, t, visibleReleaseVersions]
  );

  useEffect(() => {
    if (!supportsVersionSelection || versionMode !== 'release') return;
    if (visibleReleaseVersions.length === 0) {
      if (version) onVersionChange('');
      return;
    }
    if (visibleReleaseVersions.some((release) => release.tagName === version)) return;
    onVersionChange(visibleReleaseVersions[0].tagName);
  }, [onVersionChange, supportsVersionSelection, version, versionMode, visibleReleaseVersions]);

  if (!entry) return null;

  const title = isUpdate
    ? t('plugin_store.update_confirm_title')
    : t('plugin_store.install_confirm_title');
  const requestedVersion =
    !supportsVersionSelection || versionMode === 'latest' ? '' : version.trim();
  const displayVersion = requestedVersion || entry.version;
  const entryTitle = getStoreEntryTitle(entry, i18n.language);
  const target = displayVersion
    ? `${entryTitle} ${formatPluginVersion(displayVersion)}`
    : entryTitle;
  const message = isUpdate
    ? t('plugin_store.update_confirm_message', { target })
    : t('plugin_store.install_confirm_message', { target });
  const latestVersionLabel = entry.version
    ? formatPluginVersion(entry.version)
    : latestRelease
      ? formatPluginVersion(latestRelease.tagName)
      : t('plugin_store.install_version_latest');
  const hasPrereleaseVersions = releaseVersions.some((release) => release.prerelease);
  const releaseModeDisabled = installing || (releaseLoading && releaseVersions.length === 0);
  const manualVersionInvalid =
    supportsVersionSelection &&
    versionMode === 'manual' &&
    Boolean(version.trim()) &&
    !isValidManualReleaseTag(version);
  const currentVersionSelected =
    Boolean(requestedVersion) &&
    Boolean(entry.installedVersion) &&
    pluginVersionMatches(entry.installedVersion, requestedVersion);
  const confirmDisabled =
    (supportsVersionSelection && versionMode === 'release' && !requestedVersion) ||
    (supportsVersionSelection && versionMode === 'manual' && !isValidManualReleaseTag(version)) ||
    currentVersionSelected;

  const handleVersionModeChange = (nextMode: InstallVersionMode) => {
    if (installing) return;
    if (!supportsVersionSelection && nextMode !== 'latest') return;
    setVersionMode(nextMode);
    if (nextMode === 'latest') {
      onVersionChange('');
      return;
    }
    if (nextMode === 'release') {
      onVersionChange(visibleReleaseVersions[0]?.tagName ?? '');
      return;
    }
    onVersionChange('');
  };

  const handleClose = () => {
    if (installing) return;
    onClose();
  };

  const renderMode = (
    mode: InstallVersionMode,
    label: string,
    hint: string | null,
    disabled: boolean
  ) => (
    <label
      className={`${styles.mode} ${versionMode === mode ? styles.modeActive : ''} ${
        disabled ? styles.modeDisabled : ''
      }`}
    >
      <input
        type="radio"
        name="plugin-store-install-version-mode"
        checked={versionMode === mode}
        onChange={() => handleVersionModeChange(mode)}
        disabled={disabled}
      />
      <span className={styles.modeText}>
        <strong>{label}</strong>
        {hint ? <small>{hint}</small> : null}
      </span>
    </label>
  );

  return (
    <Modal open={Boolean(entry)} onClose={handleClose} title={title} closeDisabled={installing}>
      <div className={styles.body}>
        <p className={styles.message}>{message}</p>
        <div className={styles.field}>
          <span className={styles.fieldLabel} id="plugin-store-install-version-mode">
            {t('plugin_store.install_version_label')}
          </span>
          <div
            className={styles.modes}
            role="radiogroup"
            aria-labelledby="plugin-store-install-version-mode"
          >
            {renderMode(
              'latest',
              t('plugin_store.install_version_latest_mode'),
              t('plugin_store.install_version_latest_hint', { version: latestVersionLabel }),
              installing
            )}
            {supportsVersionSelection ? (
              <>
                {renderMode(
                  'release',
                  t('plugin_store.install_version_release_mode'),
                  // The label already says what this does; only the live loading state shows.
                  releaseLoading ? t('plugin_store.install_versions_loading') : null,
                  releaseModeDisabled
                )}
                {renderMode(
                  'manual',
                  t('plugin_store.install_version_manual_mode'),
                  t('plugin_store.install_version_manual_hint'),
                  installing
                )}
              </>
            ) : null}
          </div>

          {supportsVersionSelection && versionMode === 'release' ? (
            <div className={styles.panel}>
              {releaseError ? (
                <p className={styles.warning}>
                  {t('plugin_store.install_versions_load_failed')}: {releaseError}
                </p>
              ) : null}
              {!releaseLoading && !releaseError && releaseVersions.length === 0 ? (
                <p className={styles.hint}>{t('plugin_store.install_versions_empty')}</p>
              ) : null}
              {!releaseLoading &&
              !releaseError &&
              releaseVersions.length > 0 &&
              visibleReleaseVersions.length === 0 ? (
                <p className={styles.hint}>{t('plugin_store.install_versions_only_prerelease')}</p>
              ) : null}
              <Select
                value={version}
                options={releaseOptions}
                onChange={onVersionChange}
                placeholder={t('plugin_store.install_version_release_placeholder')}
                disabled={installing || releaseLoading || releaseOptions.length === 0}
                ariaLabel={t('plugin_store.install_version_release_select')}
              />
              {hasPrereleaseVersions ? (
                <label className={styles.checkbox}>
                  <input
                    type="checkbox"
                    checked={showPrerelease}
                    onChange={(event) => setShowPrerelease(event.target.checked)}
                    disabled={installing}
                  />
                  <span>{t('plugin_store.install_version_show_prerelease')}</span>
                </label>
              ) : null}
            </div>
          ) : null}

          {supportsVersionSelection && versionMode === 'manual' ? (
            <div className={styles.panel}>
              <Input
                id="plugin-store-install-version"
                value={version}
                onChange={(event) => onVersionChange(event.target.value)}
                placeholder={t('plugin_store.install_version_manual_placeholder')}
                disabled={installing}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={manualVersionInvalid}
              />
              {manualVersionInvalid ? (
                <p className={styles.warning}>{t('plugin_store.install_version_manual_error')}</p>
              ) : null}
            </div>
          ) : null}

          {currentVersionSelected ? (
            <p className={styles.warning}>
              {t('plugin_store.install_version_current_selected', {
                version: formatPluginVersion(entry.installedVersion),
              })}
            </p>
          ) : null}

          {supportsVersionSelection && releasePageURL ? (
            <a
              className={styles.releaseLink}
              href={releasePageURL}
              target="_blank"
              rel="noreferrer"
            >
              {t('plugin_store.install_version_releases_link')}
              <IconExternalLink size={12} />
            </a>
          ) : null}
        </div>
        <div className={styles.actions}>
          <Button variant="ghost" onClick={handleClose} disabled={installing}>
            {t('common.cancel')}
          </Button>
          <Button
            variant={isOfficialPlugin(entry) ? 'primary' : 'danger'}
            onClick={onConfirm}
            disabled={confirmDisabled}
            loading={installing}
          >
            {isUpdate ? t('plugin_store.update') : t('plugin_store.install')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
