/**
 * Dialog for the account-level "use credits after the usage limit" setting.
 * A write is explicit. An unresolved earlier write only permits same-value retries;
 * a successful retry does not prove that the earlier request has finished.
 */

import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import type { CodexCreditOverageEntry, CodexCreditOverageOperation } from '@/types';
import {
  formatCodexCreditOverageConfirmedAt,
  resolveCodexCreditOverageState,
  codexCreditOverageWriteBlocked,
  type CodexCreditOverageContext,
} from './creditOverage';
import type { CodexCreditOverageFeedback } from './useCodexCreditOverage';
import styles from './CodexCreditOverage.module.scss';

export interface CodexCreditOverageDialogProps {
  open: boolean;
  context: CodexCreditOverageContext;
  entry: CodexCreditOverageEntry | undefined;
  operation?: CodexCreditOverageOperation;
  writeDisabled?: boolean;
  contextCurrent?: boolean;
  /** Credentials in the panel that share this account user's setting. */
  sharedCredentialCount: number;
  selection: boolean | null;
  pendingTarget: boolean | null;
  feedback: CodexCreditOverageFeedback | null;
  onSelect: (value: boolean) => void;
  onSave: () => void;
  onClose: () => void;
}

export function CodexCreditOverageDialog({
  open,
  context,
  entry,
  operation,
  writeDisabled = false,
  contextCurrent = true,
  sharedCredentialCount,
  selection,
  pendingTarget,
  feedback,
  onSelect,
  onSave,
  onClose,
}: CodexCreditOverageDialogProps) {
  const { t } = useTranslation();
  const groupName = useId();
  const pending = pendingTarget !== null;
  const writesBlocked = writeDisabled || !contextCurrent;
  const saveDisabled =
    pending ||
    writesBlocked ||
    selection === null ||
    codexCreditOverageWriteBlocked(operation, selection);
  const unconfirmed =
    operation?.status === 'unconfirmed' ||
    (operation?.status === 'writing' && operation.unconfirmed);
  const state = resolveCodexCreditOverageState(entry);
  const hasBalance = context.creditsUnlimited || context.creditBalance !== null;
  const balance = context.creditsUnlimited
    ? t('codex_quota.credit_unlimited')
    : (context.creditBalance ?? '--');
  const contextMessage = !contextCurrent
    ? t('codex_quota.credit_overage_stale')
    : unconfirmed
      ? t('codex_quota.credit_overage_unconfirmed')
      : null;
  const activeFeedback = contextCurrent ? feedback : null;
  const feedbackTone = activeFeedback?.tone ?? 'warning';
  const feedbackMessage = activeFeedback?.message ?? contextMessage;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('codex_quota.credit_overage_dialog_title')}
      closeDisabled={pending}
      footer={
        <Button type="button" onClick={onSave} disabled={saveDisabled} loading={pending}>
          {t('common.save')}
        </Button>
      }
    >
      <dl className={styles.facts}>
        <div className={styles.fact}>
          <dt>{t('codex_quota.credit_overage_account')}</dt>
          <dd>{context.accountLabel}</dd>
        </div>
        {hasBalance && (
          <div className={styles.fact}>
            <dt>{t('codex_quota.credit_balance_label')}</dt>
            <dd>{balance}</dd>
          </div>
        )}
        <div className={styles.fact}>
          <dt>{t('codex_quota.credit_overage_current')}</dt>
          <dd>
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
          </dd>
        </div>
      </dl>
      <p className={styles.intro}>{t('codex_quota.credit_overage_intro')}</p>
      <p className={styles.scope}>
        {t('codex_quota.credit_overage_scope_note')}
        {sharedCredentialCount > 1 && (
          <> {t('codex_quota.credit_overage_shared_note', { total: sharedCredentialCount })}</>
        )}
      </p>
      <div
        className={styles.choices}
        role="radiogroup"
        aria-label={t('codex_quota.credit_overage_label')}
      >
        {[true, false].map((value) => (
          <label key={String(value)} className={styles.choice}>
            <input
              type="radio"
              name={groupName}
              value={String(value)}
              checked={selection === value}
              onChange={() => onSelect(value)}
              disabled={
                pending || writesBlocked || codexCreditOverageWriteBlocked(operation, value)
              }
            />
            <span>
              {t(value ? 'codex_quota.credit_overage_allow' : 'codex_quota.credit_overage_block')}
            </span>
          </label>
        ))}
      </div>
      {feedbackMessage && (
        <div
          className={styles.feedback}
          data-tone={feedbackTone}
          role={feedbackTone === 'error' ? 'alert' : 'status'}
        >
          <p>{feedbackMessage}</p>
          {activeFeedback && contextMessage && activeFeedback.message !== contextMessage && (
            <p>{contextMessage}</p>
          )}
        </div>
      )}
    </Modal>
  );
}
