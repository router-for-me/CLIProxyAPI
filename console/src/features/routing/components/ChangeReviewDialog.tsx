import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import type { RoutingChange, RoutingMember } from '@/types/routing';
import { SETTING_LABEL_KEYS, formatSettingValue, formatWeightValue } from '../format';
import styles from '../RoutingPage.module.scss';

export interface ChangeReviewDialogProps {
  open: boolean;
  changes: readonly RoutingChange[];
  members: ReadonlyMap<string, RoutingMember>;
  saving: boolean;
  onConfirm: () => void;
  onClose: () => void;
}

export function ChangeReviewDialog({
  open,
  changes,
  members,
  saving,
  onConfirm,
  onClose,
}: ChangeReviewDialogProps) {
  const { t } = useTranslation();

  const describe = (change: RoutingChange): string => {
    if (change.kind === 'setting') {
      return t('routing.change_setting', {
        name: t(SETTING_LABEL_KEYS[change.field]),
        from: formatSettingValue(t, change.field, change.from),
        to: formatSettingValue(t, change.field, change.to),
      });
    }
    const member = members.get(change.memberKey);
    let name = member?.name ?? change.memberKey;
    if (change.kind === 'priority') {
      return t('routing.change_priority', { name, from: change.from, to: change.to });
    }
    if (member && member.weights.length > 1) {
      name = `${name} · ${member.weights[change.index]?.label ?? `#${change.index + 1}`}`;
    }
    return t('routing.change_weight', {
      name,
      from: formatWeightValue(t, change.from),
      to: formatWeightValue(t, change.to),
    });
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      closeDisabled={saving}
      title={t('routing.review_title')}
      width={560}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onConfirm} loading={saving}>
            {t('routing.confirm_save')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogHint}>{t('routing.review_hint')}</p>
      <ul className={styles.changeList}>
        {changes.map((change, index) => (
          <li key={index}>{describe(change)}</li>
        ))}
      </ul>
    </Modal>
  );
}
