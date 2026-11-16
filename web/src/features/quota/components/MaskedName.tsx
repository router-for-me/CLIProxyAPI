import type { ReactElement } from 'react';
import { useTranslation } from 'react-i18next';
import styles from '@/features/quota/components/MaskedName.module.scss';
import { MASK_TEXT, maskedLabel } from '@/features/quota/maskedLabel';

export interface MaskedNameProps {
  className?: string;
  label: string;
  masked: boolean;
  ordinal: number;
}

/** An account name, or a blurred placeholder that keeps the real name out of the DOM. */
export const MaskedName = ({
  className,
  label,
  masked,
  ordinal,
}: MaskedNameProps): ReactElement => {
  const { t } = useTranslation();

  if (!masked) return <span className={className}>{label}</span>;

  return (
    <span className={className ? `${styles.wrap} ${className}` : styles.wrap}>
      <span aria-hidden="true" className={styles.masked}>
        {MASK_TEXT}
      </span>
      <span className={styles.srOnly}>{maskedLabel(t, ordinal)}</span>
    </span>
  );
};
