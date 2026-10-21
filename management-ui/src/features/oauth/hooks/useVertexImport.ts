import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNotificationStore } from '@/stores';
import { vertexApi } from '@/services/api/vertex';
import { getErrorMessage } from '@/utils/helpers';
import { notifyAuthFilesChanged } from '@/features/authFiles/authFilesEvents';

export interface VertexImportResult {
  projectId?: string;
  email?: string;
  location?: string;
  authFile?: string;
}

export interface VertexImportState {
  file?: File;
  location: string;
  loading: boolean;
  error?: string;
  result?: VertexImportResult;
}

const isJsonFile = (file: File) => file.name.toLowerCase().endsWith('.json');

/** Vertex 服务账号 JSON 导入：选文件（含拖放）→ 可选区域 → 上传。 */
export function useVertexImport(options: {
  /** 对话框开着时结果内联展示；关着（上传中途关闭）才用 toast 兜底。 */
  isFocused: () => boolean;
  onCredentialAdded: () => void;
}) {
  const { isFocused, onCredentialAdded } = options;
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [vertex, setVertex] = useState<VertexImportState>({ location: '', loading: false });

  const pickFile = (file: File | undefined) => {
    if (!file) return;
    if (!isJsonFile(file)) {
      setVertex((prev) => ({ ...prev, error: t('vertex_import.file_required') }));
      return;
    }
    setVertex((prev) => ({ ...prev, file, error: undefined, result: undefined }));
  };

  const setLocation = (location: string) => setVertex((prev) => ({ ...prev, location }));

  const importCredential = async () => {
    if (!vertex.file) {
      setVertex((prev) => ({ ...prev, error: t('vertex_import.file_required') }));
      return;
    }
    const location = vertex.location.trim();
    setVertex((prev) => ({ ...prev, loading: true, error: undefined, result: undefined }));
    try {
      const res = await vertexApi.importCredential(vertex.file, location || undefined);
      const result: VertexImportResult = {
        projectId: res.project_id,
        email: res.email,
        location: res.location,
        authFile: res['auth-file'] ?? res.auth_file,
      };
      setVertex((prev) => ({ ...prev, loading: false, result }));
      notifyAuthFilesChanged();
      onCredentialAdded();
      if (!isFocused()) showNotification(t('vertex_import.success'), 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      setVertex((prev) => ({
        ...prev,
        loading: false,
        error: message || t('notification.upload_failed'),
      }));
      if (!isFocused()) {
        showNotification(
          message
            ? `${t('notification.upload_failed')}: ${message}`
            : t('notification.upload_failed'),
          'error'
        );
      }
    }
  };

  /** 导入成功后再导入一个：清空文件与结果，保留区域。 */
  const resetForAnother = () => setVertex((prev) => ({ location: prev.location, loading: false }));

  return { vertex, pickFile, setLocation, importCredential, resetForAnother };
}
