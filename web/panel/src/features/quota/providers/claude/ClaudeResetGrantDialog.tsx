import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { resetGrantHasNoUsage, type AnthropicResetGrant } from '@/services/api/claudeResetGrants';
import { formatInstantShort, parseIsoToMs } from '@/utils/quota';
import { resetGrantKind } from './resetGrantChoice';
import styles from './ClaudeResetGrantDialog.module.scss';

type ChoiceProps = {
  grants: readonly AnthropicResetGrant[];
  selected: string | null;
  canRedeemGrant: (id: string) => boolean;
  onSelect: (id: string) => void;
};

/** Native radios retain arrow-key navigation without a second confirmation dialog. */
export function ClaudeResetGrantChoices({
  grants,
  selected,
  canRedeemGrant,
  onSelect,
}: ChoiceProps) {
  const { t } = useTranslation();
  const name = useId();
  return (
    <fieldset className={styles.choices}>
      <legend>{t('claude_reset.choose_grant')}</legend>
      {grants.length === 0 && <p>{t('claude_reset.no_grants')}</p>}
      {grants.map((grant) => {
        const expiry = parseIsoToMs(grant.endsAt);
        const available = canRedeemGrant(grant.id);
        return (
          <label key={grant.id} className={styles.choice} data-disabled={!available}>
            <input
              type="radio"
              name={name}
              value={grant.id}
              checked={selected === grant.id}
              disabled={!available}
              onChange={() => onSelect(grant.id)}
            />
            <span className={styles.details}>
              <strong>
                {t(`claude_reset.kind_${resetGrantKind(grant)}`)} · {grant.label || grant.id}
              </strong>
              <span>
                {t('claude_reset.count', { left: grant.resetsLeft, total: grant.resetsTotal })}
              </span>
              <span>
                {t('claude_reset.grant_expiry', {
                  date:
                    expiry === null ? t('claude_reset.expiry_unknown') : formatInstantShort(expiry),
                })}
              </span>
              {/* Only Fable spends differently from what its row says; keep that visible. */}
              {grant.givesBackWeekly && <span>{t('claude_reset.fable_hint')}</span>}
              {resetGrantHasNoUsage(grant) && <span>{t('claude_reset.no_usage')}</span>}
              {!available && !resetGrantHasNoUsage(grant) && (
                <span>{t('claude_reset.grant_unavailable')}</span>
              )}
            </span>
          </label>
        );
      })}
    </fieldset>
  );
}

type Props = ChoiceProps & {
  open: boolean;
  busy: boolean;
  loading: boolean;
  pendingGrantId?: string;
  message: string;
  name: string;
  onConfirm: () => void;
  onClose: () => void;
};

export function ClaudeResetGrantDialog(props: Props) {
  const { t } = useTranslation();
  const selectedGrant = props.grants.find((grant) => grant.id === props.selected);
  return (
    <Modal
      open={props.open}
      title={t('claude_reset.title')}
      onClose={props.onClose}
      closeDisabled={props.busy}
      footer={
        <div className={styles.footer}>
          {selectedGrant && (
            <p className={styles.summary} aria-live="polite">
              {t('claude_reset.selected_grant', {
                kind: t(`claude_reset.kind_${resetGrantKind(selectedGrant)}`),
                label: selectedGrant.label || selectedGrant.id,
              })}
            </p>
          )}
          <div className={styles.actions}>
            <Button variant="secondary" onClick={props.onClose} disabled={props.busy}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={props.onConfirm}
              loading={props.busy}
              disabled={!props.selected || !props.canRedeemGrant(props.selected)}
            >
              {t(props.pendingGrantId ? 'claude_reset.retry' : 'claude_reset.confirm')}
            </Button>
          </div>
        </div>
      }
    >
      <p>
        {t(props.pendingGrantId ? 'claude_reset.retry_confirm' : 'claude_reset.confirm_text', {
          name: props.name,
        })}
      </p>
      {props.loading ? (
        <p role="status" aria-busy="true">
          {t('common.loading')}
        </p>
      ) : (
        <ClaudeResetGrantChoices {...props} />
      )}
      {props.message && <p role="status">{t(`claude_reset.${props.message}`)}</p>}
    </Modal>
  );
}
