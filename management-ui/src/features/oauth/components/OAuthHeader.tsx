import { useTranslation } from 'react-i18next';
import styles from './OAuthHeader.module.scss';

export interface OAuthHeaderProps {
  providerCount: number;
  waitingCount: number;
  addedCount: number;
}

/**
 * 标题领衔 + ▍mono 遥测 meta 行（与凭证库/额度页同语汇，无 eyebrow、无页头按钮）。
 * meta 语义：等待授权数是「进行中」（墨色 + 呼吸点），本次新增是「活数据」（绿）。
 */
export function OAuthHeader({ providerCount, waitingCount, addedCount }: OAuthHeaderProps) {
  const { t } = useTranslation();

  return (
    <header className={styles.header}>
      <h1 className={styles.title} data-reveal>
        {t('nav.oauth', { defaultValue: 'OAuth' })}
      </h1>
      <p className={styles.meta} data-reveal>
        <span>{t('auth_login.meta_providers', { count: providerCount })}</span>
        {waitingCount > 0 && (
          <>
            <span className={styles.metaDot} aria-hidden="true">
              ·
            </span>
            <span className={styles.metaWaiting}>
              <span className={styles.pulse} aria-hidden="true" />
              {t('auth_login.meta_waiting', { count: waitingCount })}
            </span>
          </>
        )}
        {addedCount > 0 && (
          <>
            <span className={styles.metaDot} aria-hidden="true">
              ·
            </span>
            <span className={styles.metaAdded}>
              {t('auth_login.meta_added', { count: addedCount })}
            </span>
          </>
        )}
      </p>
    </header>
  );
}
