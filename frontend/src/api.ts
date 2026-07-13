const API_KEY = 'devin-test';
const MGMT_KEY = 'devin-test';

async function fetchApi(path: string, options: RequestInit = {}) {
  const headers: Record<string, string> = {
    'Authorization': `Bearer ${API_KEY}`,
    'Content-Type': 'application/json',
  };
  if (path.startsWith('/v0/management')) {
    headers['X-Management-Key'] = MGMT_KEY;
  }
  const res = await fetch(`/api${path}`, {
    ...options,
    headers: { ...headers, ...(options.headers || {}) },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

export interface ModelInfo {
  id: string;
  object: string;
  owned_by: string;
  created: number;
}

export interface ModelsResponse {
  data: ModelInfo[];
}

export async function getModels(): Promise<ModelsResponse> {
  return fetchApi('/v1/models');
}

export async function extractAuth(): Promise<{ providers: string[] }> {
  return fetchApi('/v0/management/extract-auth', { method: 'POST' });
}
