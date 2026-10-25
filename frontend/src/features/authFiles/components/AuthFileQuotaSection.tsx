import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useNotificationStore,
  useQuotaStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { resolveQuotaErrorMessage } from '@/utils/quota';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { isRuntimeOnlyAuthFile, type QuotaProviderType } from '@/features/authFiles/constants';
import { IconRefreshCw } from '@/components/ui/icons';
import { bindQuotaClasses } from '@/features/quota/types';
import { QUOTA_ADAPTERS, type QuotaCardState } from '@/features/quota/providers';
import { runQuotaRefresh } from '@/features/quota/quotaActions';
import {
  buildQuotaResetConfirmation,
  quotaResetParams,
} from '@/features/quota/quotaResetConfirmation';
import styles from './AuthFileQuota.module.scss';

/** Compact cards must satisfy the same quota style contract as the quota page. */
const compactQuotaClasses = bindQuotaClasses(styles, 'AuthFileQuota.module.scss');

const assertNever = (value: never): never => {
  throw new Error(`Unsupported quota type: ${value}`);
};

type QuotaMapUpdater = (
  updater: (prev: Record<string, QuotaCardState>) => Record<string, QuotaCardState>
) => void;

export type AuthFileQuotaSectionProps = {
  file: AuthFileItem;
  quotaType: QuotaProviderType;
  disableControls: boolean;
};

export function AuthFileQuotaSection(props: AuthFileQuotaSectionProps) {
  const { file, quotaType, disableControls } = props;
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [resettingQuota, setResettingQuota] = useState(false);
  const adapter = QUOTA_ADAPTERS[quotaType];
  const cacheKey = getQuotaCacheKey(file);

  const storedQuota = useQuotaStore((state) => {
    if (quotaType === 'antigravity')
      return state.antigravityQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'claude') return state.claudeQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'codex') return state.codexQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'devin') return state.devinQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'kiro') return state.kiroQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'kimi') return state.kimiQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'zcode') return state.zcodeQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'meta') return state.metaQuota[cacheKey] as QuotaCardState | undefined;
    if (quotaType === 'xai') return state.xaiQuota[cacheKey] as QuotaCardState | undefined;
    return assertNever(quotaType);
  });
  const quota = storedQuota;

  const updateQuotaState = useQuotaStore(
    (state) => state[adapter.storeSetter] as unknown as QuotaMapUpdater
  );

  const refreshQuotaForFile = useCallback(async () => {
    if (disableControls) return;
    if (isRuntimeOnlyAuthFile(file)) return;
    if (file.disabled) return;
    if (quota?.status === 'loading') return;
    await runQuotaRefresh(adapter, file, t);
  }, [adapter, disableControls, file, quota?.status, t]);

  const resetQuotaForFile = useCallback(() => {
    if (disableControls) return;
    if (isRuntimeOnlyAuthFile(file)) return;
    if (file.disabled) return;
    if (quota?.status === 'loading') return;
    if (resettingQuota) return;

    const resetQuota = adapter.resetQuota;
    if (!resetQuota) return;

    const confirmation = buildQuotaResetConfirmation(adapter, quota, file, t);
    const params = quotaResetParams(adapter, file);
    showConfirmation({
      title: t('codex_quota.reset_confirm_title', params),
      message: confirmation.message,
      confirmText: t('codex_quota.reset_confirm_button'),
      variant: 'primary',
      onConfirm: async () => {
        const cacheGeneration = captureQuotaCacheGeneration(file.name);
        setResettingQuota(true);
        try {
          const data = await resetQuota(file, t, confirmation.getOption());
          commitIfQuotaCacheCurrent(cacheGeneration, () => {
            updateQuotaState((prev) => ({
              ...prev,
              [cacheKey]: adapter.buildSuccessState(data),
            }));
            showNotification(t('codex_quota.reset_success', params), 'success');
          });
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : t('common.unknown_error');
          commitIfQuotaCacheCurrent(cacheGeneration, () => {
            showNotification(t('codex_quota.reset_failed', { ...params, message }), 'error');
          });
        } finally {
          setResettingQuota(false);
        }
      },
    });
  }, [
    adapter,
    cacheKey,
    disableControls,
    file,
    quota,
    resettingQuota,
    showConfirmation,
    showNotification,
    t,
    updateQuotaState,
  ]);

  const quotaStatus = quota?.status ?? 'idle';
  const canRefreshQuota = !disableControls && !file.disabled && !resettingQuota;
  const canUseResetQuota = canRefreshQuota && quotaStatus !== 'loading';
  const showResetQuotaAction = quota !== undefined && Boolean(adapter.canResetQuota?.(quota));
  const quotaErrorMessage = resolveQuotaErrorMessage(
    t,
    quota?.errorStatus,
    quota?.error || t('common.unknown_error')
  );

  return (
    <div className={styles.quotaSection}>
      <div className={styles.quotaHead}>
        <span className={styles.quotaTitle}>{t('auth_files.quota_section_label')}</span>
        <div className={styles.quotaTools}>
          {adapter.resetQuota && showResetQuotaAction && (
            <button
              type="button"
              className={styles.quotaToolChip}
              onClick={() => resetQuotaForFile()}
              disabled={!canUseResetQuota}
              title={t('codex_quota.reset_button')}
              aria-label={t('codex_quota.reset_button')}
            >
              {resettingQuota && (
                <IconRefreshCw size={11} className={styles.spinning} aria-hidden="true" />
              )}
              {t('codex_quota.reset_button')}
            </button>
          )}
          <button
            type="button"
            className={styles.quotaToolIcon}
            onClick={() => void refreshQuotaForFile()}
            disabled={!canRefreshQuota || quotaStatus === 'loading'}
            title={t('auth_files.quota_refresh_hint')}
            aria-label={t('auth_files.quota_refresh_single')}
          >
            <IconRefreshCw
              size={13}
              className={quotaStatus === 'loading' ? styles.spinning : undefined}
            />
          </button>
        </div>
      </div>
      {quotaStatus === 'loading' ? (
        <div className={styles.quotaMessage}>{t(`${adapter.i18nPrefix}.loading`)}</div>
      ) : quotaStatus === 'idle' ? (
        <button
          type="button"
          className={`${styles.quotaMessage} ${styles.quotaMessageAction}`}
          onClick={() => void refreshQuotaForFile()}
          disabled={!canRefreshQuota}
        >
          {t(`${adapter.i18nPrefix}.idle`)}
        </button>
      ) : quotaStatus === 'error' ? (
        <div className={styles.quotaError}>
          {t(`${adapter.i18nPrefix}.load_failed`, {
            message: quotaErrorMessage,
          })}
        </div>
      ) : quota ? (
        <adapter.Body quota={quota} classes={compactQuotaClasses} />
      ) : (
        <div className={styles.quotaMessage}>{t(`${adapter.i18nPrefix}.idle`)}</div>
      )}
    </div>
  );
}
