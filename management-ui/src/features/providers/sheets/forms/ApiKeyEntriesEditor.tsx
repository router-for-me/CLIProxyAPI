import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  IconChevronDown,
  IconEye,
  IconEyeOff,
  IconLoader2,
  IconPlus,
  IconX,
} from '@/components/ui/icons';
import { maskApiKey } from '@/utils/format';
import { MAX_CREDENTIAL_WEIGHT } from '@/utils/credentialWeight';
import type { ApiKeyEntryInput } from '../../types';
import type { ConnectivityState, ConnectivityStatus } from './useConnectivityTest';
import { ConnectivityStatusIcon } from './ConnectivityStatusIcon';
import styles from './sharedForm.module.scss';

const COLLAPSED_LIMIT = 10;

const idleStatus: ConnectivityStatus = { state: 'idle' as ConnectivityState, message: '' };

const isBlankEntry = (entry: ApiKeyEntryInput): boolean =>
  !entry.apiKey.trim() && !entry.existingApiKey?.trim();

interface ApiKeyEntriesEditorProps {
  entries: ApiKeyEntryInput[];
  removeDisabled: boolean;
  mutating: boolean;
  statuses: ConnectivityStatus[];
  isTestingAny: boolean;
  onUpdate: (idx: number, patch: Partial<ApiKeyEntryInput>) => void;
  /** Appends a new blank entry and returns its index. */
  onAdd: () => number;
  onRemove: (idx: number) => void;
  onTest: (idx: number) => void;
  onTestAll: () => void;
}

export function ApiKeyEntriesEditor({
  entries,
  removeDisabled,
  mutating,
  statuses,
  isTestingAny,
  onUpdate,
  onAdd,
  onRemove,
  onTest,
  onTestAll,
}: ApiKeyEntriesEditorProps) {
  const { t } = useTranslation();
  const [expandedIdx, setExpandedIdx] = useState<number | null>(() =>
    entries.length === 1 && isBlankEntry(entries[0]) ? 0 : null
  );
  const [showPasswords, setShowPasswords] = useState<Set<number>>(new Set());
  const [showAll, setShowAll] = useState(false);

  const togglePasswordVisibility = (idx: number) => {
    setShowPasswords((prev) => {
      const next = new Set(prev);
      if (next.has(idx)) {
        next.delete(idx);
      } else {
        next.add(idx);
      }
      return next;
    });
  };

  const handleAdd = () => {
    const idx = onAdd();
    setExpandedIdx(idx);
  };

  const handleRemove = (removeIdx: number) => {
    setShowPasswords((prev) => {
      if (!prev.size) return prev;
      const next = new Set<number>();
      prev.forEach((idx) => {
        if (idx < removeIdx) {
          next.add(idx);
        } else if (idx > removeIdx) {
          next.add(idx - 1);
        }
      });
      return next;
    });
    setExpandedIdx((prev) => {
      if (prev === null || prev === removeIdx) return null;
      return prev > removeIdx ? prev - 1 : prev;
    });
    onRemove(removeIdx);
  };

  // Newest entries first, matching the append-on-add order.
  const reversed = entries.map((entry, idx) => ({ entry, idx })).reverse();
  const visible = showAll ? reversed : reversed.slice(0, COLLAPSED_LIMIT);

  return (
    <div className={styles.entriesList}>
      <div className={`${styles.entriesToolbar} ${styles.entriesToolbarSplit}`}>
        <button type="button" className={styles.addBtn} disabled={mutating} onClick={handleAdd}>
          <IconPlus size={12} />
          <span>{t('providersPage.form.addApiKeyEntry')}</span>
        </button>
        <button
          type="button"
          className={styles.connectivityBtn}
          disabled={mutating || isTestingAny}
          onClick={onTestAll}
        >
          {isTestingAny ? (
            <span className={`${styles.statusIcon} ${styles.statusIconLoading}`}>
              <IconLoader2 size={14} />
            </span>
          ) : null}
          <span>{t('providersPage.connectivity.testAll')}</span>
        </button>
      </div>
      {visible.map(({ entry, idx }) => {
        const status = statuses[idx] ?? idleStatus;
        const expanded = expandedIdx === idx;
        const summaryKey = entry.apiKey.trim() || entry.existingApiKey?.trim() || '';
        return (
          <div key={idx} className={styles.entryCard}>
            <div className={styles.entryCardHeader}>
              <button
                type="button"
                className={styles.entryCardToggle}
                aria-expanded={expanded}
                onClick={() => setExpandedIdx(expanded ? null : idx)}
              >
                <span>{t('providersPage.form.apiKeyEntry', { index: idx + 1 })}</span>
                <span className={styles.entrySummary}>
                  {entry.proxyUrl.trim() ? (
                    <span className={styles.entryBadge} title={entry.proxyUrl}>
                      {t('providersPage.form.proxyBadge')}
                    </span>
                  ) : null}
                  <span className={styles.entrySummaryKey}>
                    {summaryKey ? maskApiKey(summaryKey) : t('providersPage.status.notConfigured')}
                  </span>
                </span>
              </button>
              <div className={styles.entryCardHeaderRight}>
                <ConnectivityStatusIcon state={status.state} />
                <button
                  type="button"
                  className={styles.connectivityBtnGhost}
                  disabled={mutating || status.state === 'loading'}
                  onClick={() => onTest(idx)}
                >
                  {status.state === 'loading' ? (
                    <span className={`${styles.statusIcon} ${styles.statusIconLoading}`}>
                      <IconLoader2 size={14} />
                    </span>
                  ) : null}
                  <span>{t('providersPage.connectivity.test')}</span>
                </button>
                <button
                  type="button"
                  className={styles.entryCardIconBtn}
                  onClick={() => setExpandedIdx(expanded ? null : idx)}
                  title={expanded ? t('common.collapse') : t('common.expand')}
                  aria-label={expanded ? t('common.collapse') : t('common.expand')}
                >
                  <IconChevronDown
                    className={[
                      styles.entryCardChevron,
                      expanded ? styles.entryCardChevronOpen : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                    size={14}
                  />
                </button>
                <button
                  type="button"
                  className={styles.removeBtn}
                  disabled={mutating || removeDisabled}
                  onClick={() => handleRemove(idx)}
                >
                  <IconX size={12} />
                </button>
              </div>
            </div>
            {status.state === 'error' ? (
              <div className={styles.connectivityError}>{status.message}</div>
            ) : null}
            {expanded ? (
              <div className={styles.entryCardBody}>
                <div className={styles.field}>
                  <label className={styles.label}>{t('providersPage.form.apiKey')}</label>
                  <div className={styles.passwordField}>
                    <input
                      className={styles.passwordInput}
                      type={showPasswords.has(idx) ? 'text' : 'password'}
                      value={entry.apiKey}
                      onChange={(e) => onUpdate(idx, { apiKey: e.target.value })}
                      autoComplete="new-password"
                      data-1p-ignore="true"
                      data-lpignore="true"
                      data-bwignore="true"
                      disabled={mutating}
                      placeholder={
                        entry.existingApiKey
                          ? t('providersPage.form.apiKeyEditPlaceholder')
                          : t('providersPage.form.apiKeyCreatePlaceholder')
                      }
                    />
                    <button
                      type="button"
                      className={styles.passwordToggle}
                      onClick={() => togglePasswordVisibility(idx)}
                      disabled={mutating}
                      aria-label={
                        showPasswords.has(idx)
                          ? t('providersPage.form.hideApiKey')
                          : t('providersPage.form.showApiKey')
                      }
                      title={
                        showPasswords.has(idx)
                          ? t('providersPage.form.hideApiKey')
                          : t('providersPage.form.showApiKey')
                      }
                    >
                      {showPasswords.has(idx) ? <IconEyeOff size={16} /> : <IconEye size={16} />}
                    </button>
                  </div>
                </div>
                <div className={styles.field}>
                  <label className={styles.label}>{t('providersPage.form.proxyUrl')}</label>
                  <input
                    className={styles.input}
                    value={entry.proxyUrl}
                    onChange={(e) => onUpdate(idx, { proxyUrl: e.target.value })}
                    disabled={mutating}
                    placeholder="http://127.0.0.1:7890"
                  />
                </div>
                <div className={styles.field}>
                  <label className={styles.label}>{t('providersPage.form.weight')}</label>
                  <input
                    className={styles.input}
                    type="number"
                    step="1"
                    max={MAX_CREDENTIAL_WEIGHT}
                    value={entry.weight ?? ''}
                    onChange={(e) =>
                      onUpdate(idx, {
                        weight: e.target.value === '' ? undefined : Number(e.target.value),
                      })
                    }
                    disabled={mutating}
                    placeholder="1"
                  />
                  <span className={styles.labelHint}>{t('providersPage.form.weightHint')}</span>
                </div>
              </div>
            ) : null}
          </div>
        );
      })}
      {entries.length > COLLAPSED_LIMIT ? (
        <button type="button" className={styles.showMoreBtn} onClick={() => setShowAll((v) => !v)}>
          {showAll
            ? t('providersPage.form.showFewerEntries')
            : t('providersPage.form.showAllEntries', { count: entries.length })}
        </button>
      ) : null}
    </div>
  );
}
