import { apiClient } from '@/services/api/client';
import { readClaudeResetGrants } from '@/services/api/claudeResetGrants';

export const RESET_GRANT_READ_DELAY_MS = 2000;

/** One delayed attempt per effect activation, never an automatic retry. */
export function scheduleResetGrantRead(start: () => void, current: () => boolean) {
  const timeout = setTimeout(() => {
    if (current()) start();
  }, RESET_GRANT_READ_DELAY_MS);
  return () => clearTimeout(timeout);
}

/** Only card reads are shared. Spending still performs its own fresh preflight reads. */
export function createResetGrantReader(
  deps = {
    revision: () => apiClient.getConnectionRevision(),
    read: readClaudeResetGrants,
  }
) {
  let session = deps.revision();
  const inFlight = new Map<string, ReturnType<typeof readClaudeResetGrants>>();
  return (authIndex: string) => {
    const revision = deps.revision();
    if (revision !== session) {
      session = revision;
      inFlight.clear();
    }
    const existing = inFlight.get(authIndex);
    if (existing) return existing;
    const request = deps
      .read(authIndex)
      .then((result) => {
        if (deps.revision() !== revision) throw new Error('session');
        return result;
      })
      .finally(() => {
        if (inFlight.get(authIndex) === request) inFlight.delete(authIndex);
      });
    inFlight.set(authIndex, request);
    return request;
  };
}

export const readResetGrantsForCard = createResetGrantReader();
