import { apiClient } from './client';
import { sharingPath } from './sharing';
import type { AuthFileItem, AuthFilesResponse } from '@/types/authFile';

export interface SharedCredential extends AuthFileItem {
  id: string;
  route_id: string;
  owner: string;
  created_by?: string;
  channel_id: string;
  channel: string;
  channel_prefix?: string;
  channel_slug: string;
  published: boolean;
  kind?: 'credential' | 'provider';
  label?: string;
  channel_index?: number;
  provider_name?: string;
  base_url?: string;
  proxy_url?: string;
  provider_models?: { name: string; alias?: string }[];
  manageable: boolean;
  claimable: boolean;
  models: string[];
  model_definitions?: { id: string; name: string }[];
  model_aliases?: unknown;
  enabled_models?: string[];
}

let credentials: SharedCredential[] = [];
let credentialRevision = -1;
export const clearSharedCredentials = () => {
  credentials = [];
};
const load = async (signal?: AbortSignal) => {
  const revision = apiClient.getConnectionRevision();
  const response = await apiClient.get<{ credentials: SharedCredential[] }>(
    sharingPath('/credentials'),
    { signal }
  );
  if (revision === apiClient.getConnectionRevision()) {
    credentials = response.credentials;
    credentialRevision = revision;
  }
  return response.credentials;
};
const find = async (name: string) => {
  if (credentialRevision !== apiClient.getConnectionRevision()) clearSharedCredentials();
  const item =
    credentials.find((file) => file.name === name || file.id === name) ??
    (await load()).find((file) => file.name === name || file.id === name);
  if (!item) throw new Error('Credential not found');
  return item;
};

const findByAuthIndex = async (authIndex: string) => {
  if (credentialRevision !== apiClient.getConnectionRevision()) clearSharedCredentials();
  // The cache holds raw hub payloads (auth_index); camelCase authIndex only
  // exists after authFilesApi normalization.
  const matches = (file: SharedCredential) =>
    String(file.authIndex ?? file['auth_index'] ?? '') === authIndex;
  return credentials.find(matches) ?? (await load()).find(matches);
};

export const sharingCredentials = {
  list: async (signal?: AbortSignal): Promise<AuthFilesResponse> => ({
    files: await load(signal),
  }),
  findByAuthIndex,
  models: async (name: string) => {
    const file = await find(name);
    return file.models.map((id) => ({ id, type: file.provider, owned_by: file.owner }));
  },
  status: async (name: string, disabled: boolean) => {
    const file = await find(name);
    await apiClient.patch(sharingPath(`/credentials/${file.route_id}`), { disabled });
    return { status: 'ok', disabled };
  },
  remove: async (names: string[]) => {
    const files: string[] = [];
    const failed: { name: string; error: string }[] = [];
    for (const name of names) {
      try {
        const file = await find(name);
        await apiClient.delete(sharingPath(`/credentials/${file.route_id}`));
        files.push(name);
      } catch (error) {
        failed.push({ name, error: error instanceof Error ? error.message : String(error) });
      }
    }
    return { status: failed.length ? 'partial' : 'ok', deleted: files.length, files, failed };
  },
  upload: async (inputs: File[]) => {
    const { useChannelStore } = await import('@/features/sharing/useChannelStore');
    const channel = useChannelStore.getState().selected;
    if (!channel) throw new Error('Select a channel before uploading credentials');
    const files: string[] = [];
    const failed: { name: string; error: string }[] = [];
    for (const file of inputs) {
      try {
        const form = new FormData();
        form.append('file', file);
        await apiClient.postForm(sharingPath('/credentials'), form, {
          params: { channel_id: channel },
        });
        files.push(file.name);
      } catch (error) {
        failed.push({
          name: file.name,
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }
    return { status: failed.length ? 'partial' : 'ok', uploaded: files.length, files, failed };
  },
  download: async (name: string): Promise<Blob> => {
    const file = await find(name);
    const response = await apiClient.getRaw(sharingPath(`/credentials/${file.route_id}/download`), {
      responseType: 'blob',
    });
    return response.data as Blob;
  },
  refresh: async (name: string) => {
    const file = await find(name);
    await apiClient.post(sharingPath(`/credentials/${file.route_id}/refresh`));
  },
  refreshAll: async () => {
    const files = await load();
    const results: { id: string; success: boolean; error?: string }[] = [];
    for (const file of files.filter((item) => item.manageable && !item.disabled)) {
      try {
        await sharingCredentials.refresh(file.name);
        results.push({ id: file.id, success: true });
      } catch (error) {
        results.push({
          id: file.id,
          success: false,
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }
    return results;
  },
  patch: async (name: string, fields: Record<string, unknown>) => {
    const file = await find(name);
    return apiClient.patch(sharingPath(`/credentials/${file.route_id}/fields`), fields);
  },
};
