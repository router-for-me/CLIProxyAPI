import axios from 'axios';
import { normalizeApiBase } from '@/utils/connection';

/** Diagnostic only: never use v0 responses as application data or a login fallback. */
export class LegacyBackendError extends Error {
  constructor() {
    super('The backend supports v0 management but not v8 management');
    this.name = 'LegacyBackendError';
  }
}

export async function probeLegacyBackend(apiBase: string, managementKey: string): Promise<boolean> {
  const base = normalizeApiBase(apiBase);
  if (!base || !managementKey) return false;

  try {
    const response = await axios.get<unknown>(`${base}/v0/management/config`, {
      headers: { Authorization: `Bearer ${managementKey}` },
      timeout: 3000,
      // This request must not affect the v8 client's auth or version events.
      maxRedirects: 0,
    });
    return (
      response.data !== null && typeof response.data === 'object' && !Array.isArray(response.data)
    );
  } catch {
    // Includes 401, 403, network errors and missing v0 routes: none proves an old backend.
    return false;
  }
}
