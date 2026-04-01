import { forwardRef, type HTMLAttributes, type PropsWithChildren, type ReactNode } from 'react';
import styles from './ConfigSection.module.scss';

interface ConfigSectionProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title: ReactNode;
  description?: ReactNode;
  indexLabel?: ReactNode;
  icon?: ReactNode;
}

export const ConfigSection = forwardRef<HTMLElement, PropsWithChildren<ConfigSectionProps>>(
  function ConfigSection(
    { title, description, indexLabel, icon, className, children, ...rest },
    ref
  ) {
    const sectionClassName = [styles.section, className].filter(Boolean).join(' ');

    return (
      <section ref={ref} className={sectionClassName} {...rest}>
        <header className={styles.header}>
          <div className={styles.titleRow}>
            {indexLabel ? <span className={styles.indexBadge}>{indexLabel}</span> : null}
            {icon ? <span className={styles.iconBadge}>{icon}</span> : null}
          </div>
          <div className={styles.headingGroup}>
            <h2 className={styles.title}>{title}</h2>
            {description ? <p className={styles.description}>{description}</p> : null}
          </div>
        </header>
        <div className={styles.content}>{children}</div>
      </section>
    );
  }
);
