import type { ReactNode } from 'react';
import { InfoTip } from '@/components/ui/Tooltip';
import { FIELDS_ROOT_CLASS } from './fields/FieldPrimitives';
import styles from './SectionCard.module.scss';

export type SectionCardProps = {
  /** 分区序号（01–07）。常用 tab 是别名视图，不传即不显示。 */
  indexLabel?: string;
  icon?: ReactNode;
  title: ReactNode;
  /** 分区说明：默认收进标题后的 InfoTip，不占版面。 */
  description?: ReactNode;
  /** 让分区说明作为可见段落留在标题下方，而不是收进 InfoTip。 */
  keepDescriptionVisible?: boolean;
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
  keepDescriptionVisible = false,
  animateIn = false,
  children,
}: SectionCardProps) {
  return (
    <section className={`${styles.card} ${animateIn ? styles.cardEnter : ''}`}>
      <header
        className={
          description && keepDescriptionVisible
            ? `${styles.header} ${styles.headerWithDescription}`
            : styles.header
        }
      >
        <div className={styles.badges}>
          {indexLabel ? <span className={styles.indexBadge}>{indexLabel}</span> : null}
          {icon ? <span className={styles.iconBadge}>{icon}</span> : null}
        </div>
        <div className={styles.heading}>
          <div className={styles.titleLine}>
            <h2 className={styles.title}>{title}</h2>
            {description && !keepDescriptionVisible ? (
              <InfoTip
                content={description}
                label={typeof title === 'string' ? title : undefined}
              />
            ) : null}
          </div>
          {description && keepDescriptionVisible ? (
            <p className={styles.description}>{description}</p>
          ) : null}
        </div>
      </header>
      <div className={`${styles.content} ${FIELDS_ROOT_CLASS}`}>{children}</div>
    </section>
  );
}
