/** Ledger preferences that survive across sessions (localStorage). */

export const QUOTA_VIEWS = ['ledger', 'cards'] as const;
export type QuotaView = (typeof QUOTA_VIEWS)[number];

const VIEW_KEY = 'papa.quota.view';
const SHOW_EMAILS_KEY = 'papa.quota.showEmails';

const read = (key: string): string | null => {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
};

const write = (key: string, value: string) => {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Storage can be unavailable (private mode); the preference just won't stick.
  }
};

export const readQuotaView = (): QuotaView => (read(VIEW_KEY) === 'cards' ? 'cards' : 'ledger');
export const writeQuotaView = (view: QuotaView) => write(VIEW_KEY, view);

export const readShowEmails = (): boolean => read(SHOW_EMAILS_KEY) === 'true';
export const writeShowEmails = (show: boolean) => write(SHOW_EMAILS_KEY, String(show));
