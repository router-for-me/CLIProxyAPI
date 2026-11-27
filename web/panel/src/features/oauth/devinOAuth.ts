export type DevinCallbackError = 'invalid' | 'state_mismatch';

// Never infer a state or manufacture a callback URL: remote users must paste the
// complete redirect from this attempt, including the server's port and TLS mode.
export function validateDevinCallback(
  input: string,
  expectedState?: string
): DevinCallbackError | undefined {
  let url: URL;
  try {
    url = new URL(input.trim());
  } catch {
    return 'invalid';
  }
  const params = url.searchParams;
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    params.getAll('state').length !== 1 ||
    !params.get('state')?.trim() ||
    !['code', 'error', 'error_description'].some((key) => params.get(key)?.trim())
  ) {
    return 'invalid';
  }
  if (!expectedState || params.get('state') !== expectedState) {
    return 'state_mismatch';
  }
  return undefined;
}
