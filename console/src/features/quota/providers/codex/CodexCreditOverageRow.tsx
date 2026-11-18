/**
 * Codex card row for the account-level "use credits after the usage limit" setting.
 * Host-owned (passed through the body's afterPlan slot) so CodexQuotaBody stays pure;
 * the layout reuses the Claude extra-usage label + state pairing.
 */

import { useTranslation } from 'react-i18next';
import type { CodexCreditOverageEntry } from '@/types';
import type { QuotaClassMap } from '../../types';
import {
  formatCodexCreditOverageConfirmedAt,
  resolveCodexCreditOverageState,
} from './creditOverage';
import styles from './CodexCreditOverage.module.scss';

export interface CodexCreditOverageRowProps {
  entry: CodexCreditOverageEntry | undefined;
  classes: QuotaClassMap;
  disabled: boolean;
  /** Credential name for the action's accessible name; cards repeat the visible label. */
  name: string;
  onManage: () => void;
}

export function CodexCreditOverageRow({
  entry,
  classes,
  disabled,
  name,
  onManage,
}: CodexCreditOverageRowProps) {
  const { t } = useTranslation();
  const state = resolveCodexCreditOverageState(entry);

  return (
    <div className={classes.quotaRow}>
      <div className={classes.quotaRowHeader}>
        <div className={classes.claudeExtraUsageTitle}>
          <span className={classes.quotaModel}>{t('codex_quota.credit_overage_label')}</span>
          <span className={styles.status} data-state={state}>
            {t(`codex_quota.credit_overage_state_${state}`)}
          </span>
          {entry?.status === 'known' && (
            <span className={styles.confirmedAt}>
              {t('codex_quota.credit_overage_confirmed_at', {
                time: formatCodexCreditOverageConfirmedAt(entry.at),
              })}
            </span>
          )}
        </div>
        <button
          type="button"
          className={styles.manage}
          onClick={onManage}
          disabled={disabled}
          aria-haspopup="dialog"
          aria-label={t('codex_quota.credit_overage_manage_aria', { name })}
        >
          {t('codex_quota.credit_overage_manage')}
        </button>
      </div>
    </div>
  );
}
