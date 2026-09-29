import type { ReactNode } from 'react';
import { FIELDS_ROOT_CLASS } from './fields/FieldPrimitives';
import styles from './SectionCard.module.scss';

export type SectionCardProps = {
  /** 分区序号（01–07）。常用 tab 是别名视图，不传即不显示。 */
  indexLabel?: string;
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  /** 仅首载入场为 true（页面挂载时用 useState 捕获），tab 切换零动画不重播。 */
  animateIn?: boolean;
  children: ReactNode;
};

/**
 * 分区卡片：自然高度纵向流（替代旧的固定高度滚动吸附轮播）。
 * 表面配方与全站卡片一致：14px 圆角 / 1px 描边 / 82% color-mix。
 */
export function SectionCard({
  indexLabel,
  icon,
  title,
  description,
  animateIn = false,
  children,
}: SectionCardProps) {
  return (
    <section className={`${styles.card} ${animateIn ? styles.cardEnter : ''}`}>
      <header className={styles.header}>
        <div className={styles.badges}>
          {indexLabel ? <span className={styles.indexBadge}>{indexLabel}</span> : null}
          {icon ? <span className={styles.iconBadge}>{icon}</span> : null}
        </div>
        <div className={styles.heading}>
          <h2 className={styles.title}>{title}</h2>
          {description ? <p className={styles.description}>{description}</p> : null}
        </div>
      </header>
      <div className={`${styles.content} ${FIELDS_ROOT_CLASS}`}>{children}</div>
    </section>
  );
}
