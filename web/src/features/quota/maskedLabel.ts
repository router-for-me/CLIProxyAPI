import type { TFunction } from 'i18next';

/** Fixed length, so the mask leaks nothing about the name it hides. */
export const MASK_TEXT = '••••••';

/** The text that stands in for a hidden account name in toasts and confirmations. */
export const maskedLabel = (t: TFunction, ordinal: number): string =>
  t('quota_pool.account_n', { n: ordinal });
