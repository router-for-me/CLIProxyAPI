const API_KEY_PREFIX = 'sk-';
const API_KEY_RANDOM_LENGTH = 48;
const API_KEY_CHARSET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
const MAX_UNBIASED_BYTE = Math.floor(256 / API_KEY_CHARSET.length) * API_KEY_CHARSET.length;

/**
 * Generates a cryptographically secure, uniformly distributed API key.
 * The resulting key is 51 characters long: `sk-` plus 48 random characters.
 */
export function generateSecureApiKey(): string {
  const characters: string[] = [];

  while (characters.length < API_KEY_RANDOM_LENGTH) {
    const remaining = API_KEY_RANDOM_LENGTH - characters.length;
    const randomBytes = new Uint8Array(Math.ceil(remaining * 1.1));
    globalThis.crypto.getRandomValues(randomBytes);

    for (const byte of randomBytes) {
      if (byte >= MAX_UNBIASED_BYTE) continue;

      characters.push(API_KEY_CHARSET[byte % API_KEY_CHARSET.length]);
      if (characters.length === API_KEY_RANDOM_LENGTH) break;
    }
  }

  return `${API_KEY_PREFIX}${characters.join('')}`;
}
