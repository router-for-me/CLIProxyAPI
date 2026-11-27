import { useTranslation } from 'react-i18next';
import { googleAccountsUrl } from '@/services/api/antigravityValidation';
import { resolveQuotaErrorMessage } from '@/utils/quota';
import type { QuotaCardState } from '../providers';

export function QuotaErrorMessage({
  quota,
  i18nPrefix,
}: {
  quota: QuotaCardState;
  i18nPrefix: string;
}) {
  const { t } = useTranslation();
  const fallback = quota.error || t('common.unknown_error');
  const message = quota.validation
    ? fallback
    : resolveQuotaErrorMessage(t, quota.errorStatus, fallback);
  const href = googleAccountsUrl(quota.validation?.url);
  return (
    <>
      {t(`${i18nPrefix}.load_failed`, { message })}
      {href && (
        <>
          {' '}
          <a
            style={{ textDecoration: 'underline' }}
            href={href}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t('quota_management.open_verification_link')}
          </a>
        </>
      )}
    </>
  );
}
