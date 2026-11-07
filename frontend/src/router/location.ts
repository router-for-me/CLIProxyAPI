import type { Location, To } from 'react-router-dom';

type RedirectState = {
  from?: Partial<Pick<Location, 'pathname' | 'search' | 'hash'>>;
};

/** Restore the complete internal route after authentication, including filters. */
export function loginRedirectTarget(state: unknown): To {
  const from = (state as RedirectState | null)?.from;
  return {
    pathname: from?.pathname || '/',
    search: from?.search || '',
    hash: from?.hash || '',
  };
}
