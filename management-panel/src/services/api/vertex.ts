/**
 * Vertex credential import API
 */

import { apiClient } from './client';

export interface VertexImportResponse {
  status: 'ok';
  project_id?: string;
  email?: string;
  location?: string;
  'auth-file'?: string;
  auth_file?: string;
}

export const vertexApi = {
  importCredential: (file: File, location?: string) => {
    const formData = new FormData();
    formData.append('file', file);
    if (location) {
      formData.append('location', location);
    }
    return apiClient.postForm<VertexImportResponse>('/vertex/import', formData);
  }
};
