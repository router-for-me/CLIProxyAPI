/** Quota page view choice (ledger rows or upstream cards), kept in localStorage. */

export const QUOTA_VIEW_KEY = 'prox.quotaView';
export type QuotaView = 'ledger' | 'cards';

export const readQuotaView = (): QuotaView => {
  try {
    return window.localStorage.getItem(QUOTA_VIEW_KEY) === 'cards' ? 'cards' : 'ledger';
  } catch {
    return 'ledger';
  }
};

export const writeQuotaView = (view: QuotaView) => {
  try {
    window.localStorage.setItem(QUOTA_VIEW_KEY, view);
  } catch {
    // ignore
  }
};
