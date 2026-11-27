import { useId, type ReactNode } from 'react';
import { IconChevronDown } from '@/components/ui/icons';
import styles from './sharedForm.module.scss';

interface FormSectionProps {
  title: string;
  children: ReactNode;
}

/** A titled run of always-visible fields. The heading names the region for assistive tech. */
export function FormSection({ title, children }: FormSectionProps) {
  const headingId = useId();
  return (
    <section className={styles.section} aria-labelledby={headingId}>
      <h3 id={headingId} className={styles.sectionHeading}>
        {title}
      </h3>
      {children}
    </section>
  );
}

interface FormDisclosureProps {
  label: string;
  /** Short summary after the label, typically a count. */
  count?: ReactNode;
  defaultOpen?: boolean;
  children: ReactNode;
}

/**
 * Settings-style disclosure row built on `<details>`: content stays in the DOM while
 * closed (find-in-page, static rendering), and hosts group rows with `.disclosureGroup`.
 */
export function FormDisclosure({
  label,
  count,
  defaultOpen = false,
  children,
}: FormDisclosureProps) {
  return (
    <details className={styles.disclosure} open={defaultOpen || undefined}>
      <summary className={styles.disclosureSummary}>
        <span className={styles.disclosureLabel}>{label}</span>
        {count !== undefined && count !== null && count !== '' ? (
          <span className={styles.disclosureCount}>{count}</span>
        ) : null}
        <IconChevronDown size={15} className={styles.disclosureChevron} aria-hidden="true" />
      </summary>
      <div className={styles.disclosureBody}>{children}</div>
    </details>
  );
}
