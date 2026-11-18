import { useId, type ReactNode } from 'react';
import { IconNetwork } from '@/components/ui/icons';
import styles from './ModelRules.module.scss';

export type RuleGroupProps = {
  provider: string;
  label: string;
  icon: string | null;
  /** mono 遥测，例如「6 条映射」。 */
  summary: string;
  dirty: boolean;
  dirtyLabel: string;
  actions?: ReactNode;
  children: ReactNode;
};

/** 一个 provider 的规则面板：两个工作台（禁用 / 别名）共用同一副外壳。 */
export function RuleGroup({
  provider,
  label,
  icon,
  summary,
  dirty,
  dirtyLabel,
  actions,
  children,
}: RuleGroupProps) {
  const headingId = useId();
  return (
    <section className={styles.group} aria-labelledby={headingId}>
      <header className={styles.groupHead}>
        <span className={styles.groupIcon} aria-hidden="true">
          {icon ? <img src={icon} alt="" /> : <IconNetwork size={16} />}
        </span>
        <div className={styles.groupCopy}>
          <h2 className={styles.groupTitle} id={headingId}>
            {label}
            {dirty ? (
              <span className={styles.groupDirty} role="img" aria-label={dirtyLabel} />
            ) : null}
          </h2>
          <p className={styles.groupSummary}>
            <span className={styles.groupKey}>{provider}</span>
            <span className={styles.metaDot} aria-hidden="true">
              ·
            </span>
            {summary}
          </p>
        </div>
        {actions ? <div className={styles.groupActions}>{actions}</div> : null}
      </header>
      {children}
    </section>
  );
}
