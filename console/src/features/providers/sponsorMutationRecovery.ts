export class SponsorPartialMutationError extends Error {
  readonly cause: unknown;

  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause ?? 'Sponsor mutation failed'));
    this.name = 'SponsorPartialMutationError';
    this.cause = cause;
  }
}

export const isSponsorPartialMutationError = (
  error: unknown
): error is SponsorPartialMutationError => error instanceof SponsorPartialMutationError;

export async function runSponsorMutationWithRecovery<T>(
  action: () => Promise<T>,
  refresh: () => Promise<unknown>
): Promise<T> {
  try {
    return await action();
  } catch (error: unknown) {
    try {
      await refresh();
    } catch {
      // Preserve the original mutation error; refresh is best-effort recovery.
    }
    throw new SponsorPartialMutationError(error);
  }
}
