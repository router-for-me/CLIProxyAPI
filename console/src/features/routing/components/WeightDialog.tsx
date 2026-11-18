import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import type { RoutingMember, RoutingStrategy } from '@/types/routing';
import { ROUTING_WEIGHT_MAX, clampWeight } from '../logic';
import styles from '../RoutingPage.module.scss';

export interface WeightDialogProps {
  open: boolean;
  member: RoutingMember | null;
  weights: readonly (number | null)[];
  strategy: RoutingStrategy;
  onApply: (key: string, weights: (number | null)[]) => void;
  onClose: () => void;
}

const toText = (weight: number | null | undefined) =>
  weight === null || weight === undefined ? '' : String(weight);

/** The caller remounts this per opening (via `key`) so fields start from the draft. */
export function WeightDialog({
  open,
  member,
  weights,
  strategy,
  onApply,
  onClose,
}: WeightDialogProps) {
  const { t } = useTranslation();
  const [values, setValues] = useState<string[]>(() =>
    (member?.weights ?? []).map((_, index) => toText(weights[index]))
  );

  if (!member) return null;

  const apply = () => {
    onApply(
      member.key,
      values.map((value) => (value.trim() === '' ? null : clampWeight(Number(value))))
    );
    onClose();
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('routing.weight_title', { name: member.name })}
      width={440}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={apply}>{t('routing.weight_apply')}</Button>
        </>
      }
    >
      <p className={styles.dialogHint}>{t('routing.weight_hint')}</p>
      {strategy !== 'weighted-round-robin' ? (
        <p className={styles.dialogNotice}>{t('routing.weight_inactive')}</p>
      ) : null}
      <div className={styles.weightList}>
        {member.weights.map((slot, index) => (
          <label key={slot.index} className={styles.weightRow}>
            <span className={styles.weightLabel}>{slot.label}</span>
            <input
              type="number"
              inputMode="numeric"
              min={0}
              max={ROUTING_WEIGHT_MAX}
              step={1}
              placeholder={t('routing.weight_placeholder')}
              value={values[index] ?? ''}
              onChange={(event) =>
                setValues((current) =>
                  current.map((value, position) =>
                    position === index ? event.target.value : value
                  )
                )
              }
              onKeyDown={(event) => {
                if (event.key === 'Enter') apply();
              }}
            />
          </label>
        ))}
      </div>
    </Modal>
  );
}
