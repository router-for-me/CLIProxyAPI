import { useId, useState } from 'react';
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
import { FieldLabel } from '@/components/ui/FieldLabel';
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
  const fid = useId();
  const [expandedIdx, setExpandedIdx] = useState<number | null>(() =>
    entries.length === 1 && isBlankEntry(entries[0]) ? 0 : null
  );
  const [showPasswords, setShowPasswords] = useState<Set<number>>(new Set());
  const [showAll, setShowAll] = useState(false);

  const togglePasswordVisibility = (idx: number) => {
    setShowPasswords((prev) => {
      const next = new Set(prev);
      if (next.has(idx)) next.delete(idx);
      else next.add(idx);
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
        if (idx < removeIdx) next.add(idx);
        else if (idx > removeIdx) next.add(idx - 1);
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
        <button
          type="button"
          className={styles.connectivityBtn}
          disabled={mutating}
          onClick={handleAdd}
        >
          <IconPlus size={13} />
          <span>{t('providersPage.form.addApiKeyEntry')}</span>
        </button>
        <button
          type="button"
          className={styles.connectivityBtnGhost}
          disabled={mutating || isTestingAny}
          onClick={onTestAll}
        >
          {isTestingAny ? (
            <span className={`${styles.statusIcon} ${styles.statusIconLoading}`}>
              <IconLoader2 size={13} />
            </span>
          ) : null}
          <span>{t('providersPage.connectivity.testAll')}</span>
        </button>
      </div>
      {visible.map(({ entry, idx }) => {
        const status = statuses[idx] ?? idleStatus;
        const expanded = expandedIdx === idx;
        const summaryKey = entry.apiKey.trim() || entry.existingApiKey?.trim() || '';
        const toggleLabel = expanded ? t('common.collapse') : t('common.expand');
        const passwordShown = showPasswords.has(idx);
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
                  {t('providersPage.connectivity.test')}
                </button>
                <button
                  type="button"
                  className={styles.entryCardIconBtn}
                  onClick={() => setExpandedIdx(expanded ? null : idx)}
                  title={toggleLabel}
                  aria-label={toggleLabel}
                  aria-expanded={expanded}
                >
                  <IconChevronDown
                    className={`${styles.entryCardChevron} ${
                      expanded ? styles.entryCardChevronOpen : ''
                    }`}
                    size={15}
                  />
                </button>
                <button
                  type="button"
                  className={styles.removeBtn}
                  disabled={mutating || removeDisabled}
                  onClick={() => handleRemove(idx)}
                  aria-label={t('providersPage.actions.delete')}
                  title={t('providersPage.actions.delete')}
                >
                  <IconX size={14} />
                </button>
              </div>
            </div>
            {status.state === 'error' ? (
              <div className={styles.connectivityError}>{status.message}</div>
            ) : null}
            {expanded ? (
              <div className={styles.entryCardBody}>
                <div className={styles.field}>
                  <label className={styles.label} htmlFor={`${fid}-${idx}-key`}>
                    {t('providersPage.form.apiKey')}
                  </label>
                  <div className={styles.passwordField}>
                    <input
                      id={`${fid}-${idx}-key`}
                      className={`${styles.input} ${styles.passwordInput}`}
                      type={passwordShown ? 'text' : 'password'}
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
                        passwordShown
                          ? t('providersPage.form.hideApiKey')
                          : t('providersPage.form.showApiKey')
                      }
                      title={
                        passwordShown
                          ? t('providersPage.form.hideApiKey')
                          : t('providersPage.form.showApiKey')
                      }
                    >
                      {passwordShown ? <IconEyeOff size={15} /> : <IconEye size={15} />}
                    </button>
                  </div>
                </div>
                <div className={styles.fieldRow}>
                  <div className={styles.field}>
                    <label className={styles.label} htmlFor={`${fid}-${idx}-proxy`}>
                      {t('providersPage.form.proxyUrl')}
                    </label>
                    <input
                      id={`${fid}-${idx}-proxy`}
                      className={styles.input}
                      value={entry.proxyUrl}
                      onChange={(e) => onUpdate(idx, { proxyUrl: e.target.value })}
                      disabled={mutating}
                      placeholder="http://127.0.0.1:7890"
                    />
                  </div>
                  <div className={styles.field}>
                    <FieldLabel
                      className={styles.labelLine}
                      labelClassName={styles.label}
                      htmlFor={`${fid}-${idx}-weight`}
                      label={t('providersPage.form.weight')}
                      hint={t('providersPage.form.weightHint')}
                      hintId={`${fid}-${idx}-weight-hint`}
                    />
                    <input
                      id={`${fid}-${idx}-weight`}
                      aria-describedby={`${fid}-${idx}-weight-hint`}
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
                  </div>
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
