import { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useInterval } from '@/hooks/useInterval';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { EmptyState } from '@/components/ui/EmptyState';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconBot,
  IconCode,
  IconDownload,
  IconInfo,
  IconTrash2,
} from '@/components/ui/icons';
import { useAuthStore, useNotificationStore, useThemeStore } from '@/stores';
import { authFilesApi, usageApi } from '@/services/api';
import { apiClient } from '@/services/api/client';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import {
  calculateStatusBarData,
  collectUsageDetails,
  normalizeUsageSourceId,
  type KeyStatBucket,
  type KeyStats,
  type UsageDetail,
} from '@/utils/usage';
import { formatFileSize } from '@/utils/format';
import styles from './AuthFilesPage.module.scss';

type ThemeColors = { bg: string; text: string; border?: string };
type TypeColorSet = { light: ThemeColors; dark?: ThemeColors };
type ResolvedTheme = 'light' | 'dark';
type AuthFileModelItem = { id: string; display_name?: string; type?: string; owned_by?: string };

// 标签类型颜色配置（对齐重构前 styles.css 的 file-type-badge 颜色）
const TYPE_COLORS: Record<string, TypeColorSet> = {
  qwen: {
    light: { bg: '#e8f5e9', text: '#2e7d32' },
    dark: { bg: '#1b5e20', text: '#81c784' },
  },
  gemini: {
    light: { bg: '#e3f2fd', text: '#1565c0' },
    dark: { bg: '#0d47a1', text: '#64b5f6' },
  },
  'gemini-cli': {
    light: { bg: '#e7efff', text: '#1e4fa3' },
    dark: { bg: '#1c3f73', text: '#a8c7ff' },
  },
  aistudio: {
    light: { bg: '#f0f2f5', text: '#2f343c' },
    dark: { bg: '#373c42', text: '#cfd3db' },
  },
  claude: {
    light: { bg: '#fce4ec', text: '#c2185b' },
    dark: { bg: '#880e4f', text: '#f48fb1' },
  },
  codex: {
    light: { bg: '#fff3e0', text: '#ef6c00' },
    dark: { bg: '#e65100', text: '#ffb74d' },
  },
  antigravity: {
    light: { bg: '#e0f7fa', text: '#006064' },
    dark: { bg: '#004d40', text: '#80deea' },
  },
  iflow: {
    light: { bg: '#f3e5f5', text: '#7b1fa2' },
    dark: { bg: '#4a148c', text: '#ce93d8' },
  },
  empty: {
    light: { bg: '#f5f5f5', text: '#616161' },
    dark: { bg: '#424242', text: '#bdbdbd' },
  },
  unknown: {
    light: { bg: '#f0f0f0', text: '#666666', border: '1px dashed #999999' },
    dark: { bg: '#3a3a3a', text: '#aaaaaa', border: '1px dashed #666666' },
  },
};

const MIN_CARD_PAGE_SIZE = 3;
const MAX_CARD_PAGE_SIZE = 30;
const MAX_AUTH_FILE_SIZE = 50 * 1024;
const AUTH_FILES_UI_STATE_KEY = 'authFilesPage.uiState';

const clampCardPageSize = (value: number) =>
  Math.min(MAX_CARD_PAGE_SIZE, Math.max(MIN_CARD_PAGE_SIZE, Math.round(value)));

type AuthFilesUiState = {
  filter?: string;
  search?: string;
  page?: number;
  pageSize?: number;
};

const readAuthFilesUiState = (): AuthFilesUiState | null => {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.sessionStorage.getItem(AUTH_FILES_UI_STATE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as AuthFilesUiState;
    return parsed && typeof parsed === 'object' ? parsed : null;
  } catch {
    return null;
  }
};

const writeAuthFilesUiState = (state: AuthFilesUiState) => {
  if (typeof window === 'undefined') return;
  try {
    window.sessionStorage.setItem(AUTH_FILES_UI_STATE_KEY, JSON.stringify(state));
  } catch {
    // ignore
  }
};

interface PrefixProxyEditorState {
  fileName: string;
  loading: boolean;
  saving: boolean;
  error: string | null;
  originalText: string;
  rawText: string;
  json: Record<string, unknown> | null;
  prefix: string;
  proxyUrl: string;
}
// 标准化 auth_index 值（与 usage.ts 中的 normalizeAuthIndex 保持一致）
function normalizeAuthIndexValue(value: unknown): string | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value.toString();
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  return null;
}

function isRuntimeOnlyAuthFile(file: AuthFileItem): boolean {
  const raw = file['runtime_only'] ?? file.runtimeOnly;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
}

// 解析认证文件的统计数据
function resolveAuthFileStats(file: AuthFileItem, stats: KeyStats): KeyStatBucket {
  const defaultStats: KeyStatBucket = { success: 0, failure: 0 };
  const rawFileName = file?.name || '';

  // 兼容 auth_index 和 authIndex 两种字段名（API 返回的是 auth_index）
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndexKey = normalizeAuthIndexValue(rawAuthIndex);

  // 尝试根据 authIndex 匹配
  if (authIndexKey && stats.byAuthIndex?.[authIndexKey]) {
    return stats.byAuthIndex[authIndexKey];
  }

  // 尝试根据 source (文件名) 匹配
  const fileNameId = rawFileName ? normalizeUsageSourceId(rawFileName) : '';
  if (fileNameId && stats.bySource?.[fileNameId]) {
    const fromName = stats.bySource[fileNameId];
    if (fromName.success > 0 || fromName.failure > 0) {
      return fromName;
    }
  }

  // 尝试去掉扩展名后匹配
  if (rawFileName) {
    const nameWithoutExt = rawFileName.replace(/\.[^/.]+$/, '');
    if (nameWithoutExt && nameWithoutExt !== rawFileName) {
      const nameWithoutExtId = normalizeUsageSourceId(nameWithoutExt);
      const fromNameWithoutExt = nameWithoutExtId ? stats.bySource?.[nameWithoutExtId] : undefined;
      if (
        fromNameWithoutExt &&
        (fromNameWithoutExt.success > 0 || fromNameWithoutExt.failure > 0)
      ) {
        return fromNameWithoutExt;
      }
    }
  }

  return defaultStats;
}

export function AuthFilesPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const navigate = useNavigate();

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [filter, setFilter] = useState<'all' | string>('all');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(9);
  const [pageSizeInput, setPageSizeInput] = useState('9');
  const [uploading, setUploading] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [deletingAll, setDeletingAll] = useState(false);
  const [statusUpdating, setStatusUpdating] = useState<Record<string, boolean>>({});
  const [keyStats, setKeyStats] = useState<KeyStats>({ bySource: {}, byAuthIndex: {} });
  const [usageDetails, setUsageDetails] = useState<UsageDetail[]>([]);

  // 详情弹窗相关
  const [detailModalOpen, setDetailModalOpen] = useState(false);
  const [selectedFile, setSelectedFile] = useState<AuthFileItem | null>(null);

  // 模型列表弹窗相关
  const [modelsModalOpen, setModelsModalOpen] = useState(false);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsList, setModelsList] = useState<AuthFileModelItem[]>([]);
  const [modelsFileName, setModelsFileName] = useState('');
  const [modelsFileType, setModelsFileType] = useState('');
  const [modelsError, setModelsError] = useState<'unsupported' | null>(null);
  const modelsCacheRef = useRef<Map<string, AuthFileModelItem[]>>(new Map());

  // OAuth 排除模型相关
  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const [excludedError, setExcludedError] = useState<'unsupported' | null>(null);

  // OAuth 模型映射相关
  const [modelAlias, setModelAlias] = useState<Record<string, OAuthModelAliasEntry[]>>({});
  const [modelAliasError, setModelAliasError] = useState<'unsupported' | null>(null);

  const [prefixProxyEditor, setPrefixProxyEditor] = useState<PrefixProxyEditorState | null>(null);

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const loadingKeyStatsRef = useRef(false);
  const excludedUnsupportedRef = useRef(false);
  const mappingsUnsupportedRef = useRef(false);

  const normalizeProviderKey = (value: string) => value.trim().toLowerCase();

  const disableControls = connectionStatus !== 'connected';

  useEffect(() => {
    const persisted = readAuthFilesUiState();
    if (!persisted) return;

    if (typeof persisted.filter === 'string' && persisted.filter.trim()) {
      setFilter(persisted.filter);
    }
    if (typeof persisted.search === 'string') {
      setSearch(persisted.search);
    }
    if (typeof persisted.page === 'number' && Number.isFinite(persisted.page)) {
      setPage(Math.max(1, Math.round(persisted.page)));
    }
    if (typeof persisted.pageSize === 'number' && Number.isFinite(persisted.pageSize)) {
      setPageSize(clampCardPageSize(persisted.pageSize));
    }
  }, []);

  useEffect(() => {
    writeAuthFilesUiState({ filter, search, page, pageSize });
  }, [filter, search, page, pageSize]);

  useEffect(() => {
    setPageSizeInput(String(pageSize));
  }, [pageSize]);

  const prefixProxyUpdatedText = useMemo(() => {
    if (!prefixProxyEditor?.json) return prefixProxyEditor?.rawText ?? '';
    const next: Record<string, unknown> = { ...prefixProxyEditor.json };
    if ('prefix' in next || prefixProxyEditor.prefix.trim()) {
      next.prefix = prefixProxyEditor.prefix;
    }
    if ('proxy_url' in next || prefixProxyEditor.proxyUrl.trim()) {
      next.proxy_url = prefixProxyEditor.proxyUrl;
    }
    return JSON.stringify(next);
  }, [
    prefixProxyEditor?.json,
    prefixProxyEditor?.prefix,
    prefixProxyEditor?.proxyUrl,
    prefixProxyEditor?.rawText,
  ]);

  const prefixProxyDirty = useMemo(() => {
    if (!prefixProxyEditor?.json) return false;
    if (!prefixProxyEditor.originalText) return false;
    return prefixProxyUpdatedText !== prefixProxyEditor.originalText;
  }, [prefixProxyEditor?.json, prefixProxyEditor?.originalText, prefixProxyUpdatedText]);

  const commitPageSizeInput = (rawValue: string) => {
    const trimmed = rawValue.trim();
    if (!trimmed) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const value = Number(trimmed);
    if (!Number.isFinite(value)) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const next = clampCardPageSize(value);
    setPageSize(next);
    setPageSizeInput(String(next));
    setPage(1);
  };

  const handlePageSizeChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const rawValue = event.currentTarget.value;
    setPageSizeInput(rawValue);

    const trimmed = rawValue.trim();
    if (!trimmed) return;

    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) return;

    const rounded = Math.round(parsed);
    if (rounded < MIN_CARD_PAGE_SIZE || rounded > MAX_CARD_PAGE_SIZE) return;

    setPageSize(rounded);
    setPage(1);
  };

  // 格式化修改时间
  const formatModified = (item: AuthFileItem): string => {
    const raw = item['modtime'] ?? item.modified;
    if (!raw) return '-';
    const asNumber = Number(raw);
    const date =
      Number.isFinite(asNumber) && !Number.isNaN(asNumber)
        ? new Date(asNumber < 1e12 ? asNumber * 1000 : asNumber)
        : new Date(String(raw));
    return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString();
  };

  // 加载文件列表
  const loadFiles = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await authFilesApi.list();
      setFiles(data?.files || []);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(errorMessage);
    } finally {
      setLoading(false);
    }
  }, [t]);

  // 加载 key 统计和 usage 明细（API 层已有60秒超时）
  const loadKeyStats = useCallback(async () => {
    // 防止重复请求
    if (loadingKeyStatsRef.current) return;
    loadingKeyStatsRef.current = true;
    try {
      const usageResponse = await usageApi.getUsage();
      const usageData = usageResponse?.usage ?? usageResponse;
      const stats = await usageApi.getKeyStats(usageData);
      setKeyStats(stats);
      // 收集 usage 明细用于状态栏
      const details = collectUsageDetails(usageData);
      setUsageDetails(details);
    } catch {
      // 静默失败
    } finally {
      loadingKeyStatsRef.current = false;
    }
  }, []);

  // 加载 OAuth 排除列表
  const loadExcluded = useCallback(async () => {
    try {
      const res = await authFilesApi.getOauthExcludedModels();
      excludedUnsupportedRef.current = false;
      setExcluded(res || {});
      setExcludedError(null);
    } catch (err: unknown) {
      const status =
        typeof err === 'object' && err !== null && 'status' in err
          ? (err as { status?: unknown }).status
          : undefined;

      if (status === 404) {
        setExcluded({});
        setExcludedError('unsupported');
        if (!excludedUnsupportedRef.current) {
          excludedUnsupportedRef.current = true;
          showNotification(t('oauth_excluded.upgrade_required'), 'warning');
        }
        return;
      }
      // 静默失败
    }
  }, [showNotification, t]);

  // 加载 OAuth 模型映射
  const loadModelAlias = useCallback(async () => {
    try {
      const res = await authFilesApi.getOauthModelAlias();
      mappingsUnsupportedRef.current = false;
      setModelAlias(res || {});
      setModelAliasError(null);
    } catch (err: unknown) {
      const status =
        typeof err === 'object' && err !== null && 'status' in err
          ? (err as { status?: unknown }).status
          : undefined;

      if (status === 404) {
        setModelAlias({});
        setModelAliasError('unsupported');
        if (!mappingsUnsupportedRef.current) {
          mappingsUnsupportedRef.current = true;
          showNotification(t('oauth_model_alias.upgrade_required'), 'warning');
        }
        return;
      }
      // 静默失败
    }
  }, [showNotification, t]);

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([loadFiles(), loadKeyStats(), loadExcluded(), loadModelAlias()]);
  }, [loadFiles, loadKeyStats, loadExcluded, loadModelAlias]);

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    loadFiles();
    loadKeyStats();
    loadExcluded();
    loadModelAlias();
  }, [loadFiles, loadKeyStats, loadExcluded, loadModelAlias]);

  // 定时刷新状态数据（每240秒）
  useInterval(loadKeyStats, 240_000);

  // 提取所有存在的类型
  const existingTypes = useMemo(() => {
    const types = new Set<string>(['all']);
    files.forEach((file) => {
      if (file.type) {
        types.add(file.type);
      }
    });
    return Array.from(types);
  }, [files]);

  const typeCounts = useMemo(() => {
    const counts: Record<string, number> = { all: files.length };
    files.forEach((file) => {
      if (!file.type) return;
      counts[file.type] = (counts[file.type] || 0) + 1;
    });
    return counts;
  }, [files]);

  // 过滤和搜索
  const filtered = useMemo(() => {
    return files.filter((item) => {
      const matchType = filter === 'all' || item.type === filter;
      const term = search.trim().toLowerCase();
      const matchSearch =
        !term ||
        item.name.toLowerCase().includes(term) ||
        (item.type || '').toString().toLowerCase().includes(term) ||
        (item.provider || '').toString().toLowerCase().includes(term);
      return matchType && matchSearch;
    });
  }, [files, filter, search]);

  // 分页计算
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const start = (currentPage - 1) * pageSize;
  const pageItems = filtered.slice(start, start + pageSize);

  // 点击上传
  const handleUploadClick = () => {
    fileInputRef.current?.click();
  };

  // 处理文件上传（支持多选）
  const handleFileChange = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;

    const filesToUpload = Array.from(fileList);
    const validFiles: File[] = [];
    const invalidFiles: string[] = [];
    const oversizedFiles: string[] = [];

    filesToUpload.forEach((file) => {
      if (!file.name.endsWith('.json')) {
        invalidFiles.push(file.name);
        return;
      }
      if (file.size > MAX_AUTH_FILE_SIZE) {
        oversizedFiles.push(file.name);
        return;
      }
      validFiles.push(file);
    });

    if (invalidFiles.length > 0) {
      showNotification(t('auth_files.upload_error_json'), 'error');
    }
    if (oversizedFiles.length > 0) {
      showNotification(
        t('auth_files.upload_error_size', { maxSize: formatFileSize(MAX_AUTH_FILE_SIZE) }),
        'error'
      );
    }

    if (validFiles.length === 0) {
      event.target.value = '';
      return;
    }

    setUploading(true);
    let successCount = 0;
    const failed: { name: string; message: string }[] = [];

    for (const file of validFiles) {
      try {
        await authFilesApi.upload(file);
        successCount++;
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : 'Unknown error';
        failed.push({ name: file.name, message: errorMessage });
      }
    }

    if (successCount > 0) {
      const suffix = validFiles.length > 1 ? ` (${successCount}/${validFiles.length})` : '';
      showNotification(
        `${t('auth_files.upload_success')}${suffix}`,
        failed.length ? 'warning' : 'success'
      );
      await loadFiles();
      await loadKeyStats();
    }

    if (failed.length > 0) {
      const details = failed.map((item) => `${item.name}: ${item.message}`).join('; ');
      showNotification(`${t('notification.upload_failed')}: ${details}`, 'error');
    }

    setUploading(false);
    event.target.value = '';
  };

  // 删除单个文件
  const handleDelete = async (name: string) => {
    showConfirmation({
      title: t('auth_files.delete_title', { defaultValue: 'Delete File' }),
      message: `${t('auth_files.delete_confirm')} "${name}" ?`,
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        setDeleting(name);
        try {
          await authFilesApi.deleteFile(name);
          showNotification(t('auth_files.delete_success'), 'success');
          setFiles((prev) => prev.filter((item) => item.name !== name));
        } catch (err: unknown) {
          const errorMessage = err instanceof Error ? err.message : '';
          showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
        } finally {
          setDeleting(null);
        }
      },
    });
  };

  // 删除全部（根据筛选类型）
  const handleDeleteAll = async () => {
    const isFiltered = filter !== 'all';
    const typeLabel = isFiltered ? getTypeLabel(filter) : t('auth_files.filter_all');
    const confirmMessage = isFiltered
      ? t('auth_files.delete_filtered_confirm', { type: typeLabel })
      : t('auth_files.delete_all_confirm');

    showConfirmation({
      title: t('auth_files.delete_all_title', { defaultValue: 'Delete All Files' }),
      message: confirmMessage,
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        setDeletingAll(true);
        try {
          if (!isFiltered) {
            // 删除全部
            await authFilesApi.deleteAll();
            showNotification(t('auth_files.delete_all_success'), 'success');
            setFiles((prev) => prev.filter((file) => isRuntimeOnlyAuthFile(file)));
          } else {
            // 删除筛选类型的文件
            const filesToDelete = files.filter((f) => f.type === filter && !isRuntimeOnlyAuthFile(f));

            if (filesToDelete.length === 0) {
              showNotification(t('auth_files.delete_filtered_none', { type: typeLabel }), 'info');
              setDeletingAll(false);
              return;
            }

            let success = 0;
            let failed = 0;
            const deletedNames: string[] = [];

            for (const file of filesToDelete) {
              try {
                await authFilesApi.deleteFile(file.name);
                success++;
                deletedNames.push(file.name);
              } catch {
                failed++;
              }
            }

            setFiles((prev) => prev.filter((f) => !deletedNames.includes(f.name)));

            if (failed === 0) {
              showNotification(
                t('auth_files.delete_filtered_success', { count: success, type: typeLabel }),
                'success'
              );
            } else {
              showNotification(
                t('auth_files.delete_filtered_partial', { success, failed, type: typeLabel }),
                'warning'
              );
            }
            setFilter('all');
          }
        } catch (err: unknown) {
          const errorMessage = err instanceof Error ? err.message : '';
          showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
        } finally {
          setDeletingAll(false);
        }
      },
    });
  };

  // 下载文件
  const handleDownload = async (name: string) => {
    try {
      const response = await apiClient.getRaw(
        `/auth-files/download?name=${encodeURIComponent(name)}`,
        {
          responseType: 'blob',
        }
      );
      const blob = new Blob([response.data]);
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = name;
      a.click();
      window.URL.revokeObjectURL(url);
      showNotification(t('auth_files.download_success'), 'success');
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.download_failed')}: ${errorMessage}`, 'error');
    }
  };

  const openPrefixProxyEditor = async (name: string) => {
    if (disableControls) return;
    if (prefixProxyEditor?.fileName === name) {
      setPrefixProxyEditor(null);
      return;
    }

    setPrefixProxyEditor({
      fileName: name,
      loading: true,
      saving: false,
      error: null,
      originalText: '',
      rawText: '',
      json: null,
      prefix: '',
      proxyUrl: '',
    });

    try {
      const rawText = await authFilesApi.downloadText(name);
      const trimmed = rawText.trim();

      let parsed: unknown;
      try {
        parsed = JSON.parse(trimmed) as unknown;
      } catch {
        setPrefixProxyEditor((prev) => {
          if (!prev || prev.fileName !== name) return prev;
          return {
            ...prev,
            loading: false,
            error: t('auth_files.prefix_proxy_invalid_json'),
            rawText: trimmed,
            originalText: trimmed,
          };
        });
        return;
      }

      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setPrefixProxyEditor((prev) => {
          if (!prev || prev.fileName !== name) return prev;
          return {
            ...prev,
            loading: false,
            error: t('auth_files.prefix_proxy_invalid_json'),
            rawText: trimmed,
            originalText: trimmed,
          };
        });
        return;
      }

      const json = parsed as Record<string, unknown>;
      const originalText = JSON.stringify(json);
      const prefix = typeof json.prefix === 'string' ? json.prefix : '';
      const proxyUrl = typeof json.proxy_url === 'string' ? json.proxy_url : '';

      setPrefixProxyEditor((prev) => {
        if (!prev || prev.fileName !== name) return prev;
        return {
          ...prev,
          loading: false,
          originalText,
          rawText: originalText,
          json,
          prefix,
          proxyUrl,
          error: null,
        };
      });
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.download_failed');
      setPrefixProxyEditor((prev) => {
        if (!prev || prev.fileName !== name) return prev;
        return { ...prev, loading: false, error: errorMessage, rawText: '' };
      });
      showNotification(`${t('notification.download_failed')}: ${errorMessage}`, 'error');
    }
  };

  const handlePrefixProxyChange = (field: 'prefix' | 'proxyUrl', value: string) => {
    setPrefixProxyEditor((prev) => {
      if (!prev) return prev;
      if (field === 'prefix') return { ...prev, prefix: value };
      return { ...prev, proxyUrl: value };
    });
  };

  const handlePrefixProxySave = async () => {
    if (!prefixProxyEditor?.json) return;
    if (!prefixProxyDirty) return;

    const name = prefixProxyEditor.fileName;
    const payload = prefixProxyUpdatedText;
    const fileSize = new Blob([payload]).size;
    if (fileSize > MAX_AUTH_FILE_SIZE) {
      showNotification(
        t('auth_files.upload_error_size', { maxSize: formatFileSize(MAX_AUTH_FILE_SIZE) }),
        'error'
      );
      return;
    }

    setPrefixProxyEditor((prev) => {
      if (!prev || prev.fileName !== name) return prev;
      return { ...prev, saving: true };
    });

    try {
      const file = new File([payload], name, { type: 'application/json' });
      await authFilesApi.upload(file);
      showNotification(t('auth_files.prefix_proxy_saved_success', { name }), 'success');
      await loadFiles();
      await loadKeyStats();
      setPrefixProxyEditor(null);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.upload_failed')}: ${errorMessage}`, 'error');
      setPrefixProxyEditor((prev) => {
        if (!prev || prev.fileName !== name) return prev;
        return { ...prev, saving: false };
      });
    }
  };

  const handleStatusToggle = async (item: AuthFileItem, enabled: boolean) => {
    const name = item.name;
    const nextDisabled = !enabled;
    const previousDisabled = item.disabled === true;

    setStatusUpdating((prev) => ({ ...prev, [name]: true }));
    // Optimistic update for snappy UI.
    setFiles((prev) => prev.map((f) => (f.name === name ? { ...f, disabled: nextDisabled } : f)));

    try {
      const res = await authFilesApi.setStatus(name, nextDisabled);
      setFiles((prev) => prev.map((f) => (f.name === name ? { ...f, disabled: res.disabled } : f)));
      showNotification(
        enabled
          ? t('auth_files.status_enabled_success', { name })
          : t('auth_files.status_disabled_success', { name }),
        'success'
      );
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : '';
      setFiles((prev) =>
        prev.map((f) => (f.name === name ? { ...f, disabled: previousDisabled } : f))
      );
      showNotification(`${t('notification.update_failed')}: ${errorMessage}`, 'error');
    } finally {
      setStatusUpdating((prev) => {
        if (!prev[name]) return prev;
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  };

  // 显示详情弹窗
  const showDetails = (file: AuthFileItem) => {
    setSelectedFile(file);
    setDetailModalOpen(true);
  };

  // 显示模型列表
  const showModels = async (item: AuthFileItem) => {
    setModelsFileName(item.name);
    setModelsFileType(item.type || '');
    setModelsList([]);
    setModelsError(null);
    setModelsModalOpen(true);

    const cached = modelsCacheRef.current.get(item.name);
    if (cached) {
      setModelsList(cached);
      setModelsLoading(false);
      return;
    }

    setModelsLoading(true);
    try {
      const models = await authFilesApi.getModelsForAuthFile(item.name);
      modelsCacheRef.current.set(item.name, models);
      setModelsList(models);
    } catch (err) {
      // 检测是否是 API 不支持的错误 (404 或特定错误消息)
      const errorMessage = err instanceof Error ? err.message : '';
      if (
        errorMessage.includes('404') ||
        errorMessage.includes('not found') ||
        errorMessage.includes('Not Found')
      ) {
        setModelsError('unsupported');
      } else {
        showNotification(`${t('notification.load_failed')}: ${errorMessage}`, 'error');
      }
    } finally {
      setModelsLoading(false);
    }
  };

  // 检查模型是否被 OAuth 排除
  const isModelExcluded = (modelId: string, providerType: string): boolean => {
    const providerKey = normalizeProviderKey(providerType);
    const excludedModels = excluded[providerKey] || excluded[providerType] || [];
    return excludedModels.some((pattern) => {
      if (pattern.includes('*')) {
        // 支持通配符匹配
        const regex = new RegExp('^' + pattern.replace(/\*/g, '.*') + '$', 'i');
        return regex.test(modelId);
      }
      return pattern.toLowerCase() === modelId.toLowerCase();
    });
  };

  // 获取类型标签显示文本
  const getTypeLabel = (type: string): string => {
    const key = `auth_files.filter_${type}`;
    const translated = t(key);
    if (translated !== key) return translated;
    if (type.toLowerCase() === 'iflow') return 'iFlow';
    return type.charAt(0).toUpperCase() + type.slice(1);
  };

  // 获取类型颜色
  const getTypeColor = (type: string): ThemeColors => {
    const set = TYPE_COLORS[type] || TYPE_COLORS.unknown;
    return resolvedTheme === 'dark' && set.dark ? set.dark : set.light;
  };

  const openExcludedEditor = (provider?: string) => {
    const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
    const params = new URLSearchParams();
    if (providerValue) {
      params.set('provider', providerValue);
    }
    const search = params.toString();
    navigate(`/auth-files/oauth-excluded${search ? `?${search}` : ''}`, {
      state: { fromAuthFiles: true },
    });
  };

  const deleteExcluded = async (provider: string) => {
    const providerLabel = provider.trim() || provider;
    showConfirmation({
      title: t('oauth_excluded.delete_title', { defaultValue: 'Delete Exclusion' }),
      message: t('oauth_excluded.delete_confirm', { provider: providerLabel }),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        const providerKey = normalizeProviderKey(provider);
        if (!providerKey) {
          showNotification(t('oauth_excluded.provider_required'), 'error');
          return;
        }
        try {
          await authFilesApi.deleteOauthExcludedEntry(providerKey);
          await loadExcluded();
          showNotification(t('oauth_excluded.delete_success'), 'success');
        } catch (err: unknown) {
          try {
            const current = await authFilesApi.getOauthExcludedModels();
            const next: Record<string, string[]> = {};
            Object.entries(current).forEach(([key, models]) => {
              if (normalizeProviderKey(key) === providerKey) return;
              next[key] = models;
            });
            await authFilesApi.replaceOauthExcludedModels(next);
            await loadExcluded();
            showNotification(t('oauth_excluded.delete_success'), 'success');
          } catch (fallbackErr: unknown) {
            const errorMessage =
              fallbackErr instanceof Error
                ? fallbackErr.message
                : err instanceof Error
                  ? err.message
                  : '';
            showNotification(`${t('oauth_excluded.delete_failed')}: ${errorMessage}`, 'error');
          }
        }
      },
    });
  };

  const openModelAliasEditor = (provider?: string) => {
    const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
    const params = new URLSearchParams();
    if (providerValue) {
      params.set('provider', providerValue);
    }
    const search = params.toString();
    navigate(`/auth-files/oauth-model-alias${search ? `?${search}` : ''}`, {
      state: { fromAuthFiles: true },
    });
  };

  const deleteModelAlias = async (provider: string) => {
    showConfirmation({
      title: t('oauth_model_alias.delete_title', { defaultValue: 'Delete Mappings' }),
      message: t('oauth_model_alias.delete_confirm', { provider }),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await authFilesApi.deleteOauthModelAlias(provider);
          await loadModelAlias();
          showNotification(t('oauth_model_alias.delete_success'), 'success');
        } catch (err: unknown) {
          const errorMessage = err instanceof Error ? err.message : '';
          showNotification(`${t('oauth_model_alias.delete_failed')}: ${errorMessage}`, 'error');
        }
      },
    });
  };

  // 渲染标签筛选器
  const renderFilterTags = () => (
    <div className={styles.filterTags}>
      {existingTypes.map((type) => {
        const isActive = filter === type;
        const color =
          type === 'all'
            ? { bg: 'var(--bg-tertiary)', text: 'var(--text-primary)' }
            : getTypeColor(type);
        const activeTextColor = resolvedTheme === 'dark' ? '#111827' : '#fff';
        return (
          <button
            key={type}
            className={`${styles.filterTag} ${isActive ? styles.filterTagActive : ''}`}
            style={{
              backgroundColor: isActive ? color.text : color.bg,
              color: isActive ? activeTextColor : color.text,
              borderColor: color.text,
            }}
            onClick={() => {
              setFilter(type);
              setPage(1);
            }}
          >
            <span className={styles.filterTagLabel}>{getTypeLabel(type)}</span>
            <span className={styles.filterTagCount}>{typeCounts[type] ?? 0}</span>
          </button>
        );
      })}
    </div>
  );

  // 预计算所有认证文件的状态栏数据（避免每次渲染重复计算）
  const statusBarCache = useMemo(() => {
    const cache = new Map<string, ReturnType<typeof calculateStatusBarData>>();

    files.forEach((file) => {
      const rawAuthIndex = file['auth_index'] ?? file.authIndex;
      const authIndexKey = normalizeAuthIndexValue(rawAuthIndex);

      if (authIndexKey) {
        // 过滤出属于该认证文件的 usage 明细
        const filteredDetails = usageDetails.filter((detail) => {
          const detailAuthIndex = normalizeAuthIndexValue(detail.auth_index);
          return detailAuthIndex !== null && detailAuthIndex === authIndexKey;
        });
        cache.set(authIndexKey, calculateStatusBarData(filteredDetails));
      }
    });

    return cache;
  }, [usageDetails, files]);

  // 渲染状态监测栏
  const renderStatusBar = (item: AuthFileItem) => {
    // 认证文件使用 authIndex 来匹配 usage 数据
    const rawAuthIndex = item['auth_index'] ?? item.authIndex;
    const authIndexKey = normalizeAuthIndexValue(rawAuthIndex);

    const statusData =
      (authIndexKey && statusBarCache.get(authIndexKey)) || calculateStatusBarData([]);
    const hasData = statusData.totalSuccess + statusData.totalFailure > 0;
    const rateClass = !hasData
      ? ''
      : statusData.successRate >= 90
        ? styles.statusRateHigh
        : statusData.successRate >= 50
          ? styles.statusRateMedium
          : styles.statusRateLow;

    return (
      <div className={styles.statusBar}>
        <div className={styles.statusBlocks}>
          {statusData.blocks.map((state, idx) => {
            const blockClass =
              state === 'success'
                ? styles.statusBlockSuccess
                : state === 'failure'
                  ? styles.statusBlockFailure
                  : state === 'mixed'
                    ? styles.statusBlockMixed
                    : styles.statusBlockIdle;
            return <div key={idx} className={`${styles.statusBlock} ${blockClass}`} />;
          })}
        </div>
        <span className={`${styles.statusRate} ${rateClass}`}>
          {hasData ? `${statusData.successRate.toFixed(1)}%` : '--'}
        </span>
      </div>
    );
  };

  // 渲染单个认证文件卡片
	  const renderFileCard = (item: AuthFileItem) => {
	    const fileStats = resolveAuthFileStats(item, keyStats);
	    const isRuntimeOnly = isRuntimeOnlyAuthFile(item);
	    const isAistudio = (item.type || '').toLowerCase() === 'aistudio';
	    const showModelsButton = !isRuntimeOnly || isAistudio;
	    const typeColor = getTypeColor(item.type || 'unknown');

	    return (
	      <div
	        key={item.name}
	        className={`${styles.fileCard} ${item.disabled ? styles.fileCardDisabled : ''}`}
	      >
	        <div className={styles.cardHeader}>
	          <span
	            className={styles.typeBadge}
	            style={{
              backgroundColor: typeColor.bg,
              color: typeColor.text,
              ...(typeColor.border ? { border: typeColor.border } : {}),
            }}
          >
            {getTypeLabel(item.type || 'unknown')}
          </span>
          <span className={styles.fileName}>{item.name}</span>
        </div>

        <div className={styles.cardMeta}>
          <span>
            {t('auth_files.file_size')}: {item.size ? formatFileSize(item.size) : '-'}
          </span>
          <span>
            {t('auth_files.file_modified')}: {formatModified(item)}
          </span>
        </div>

        <div className={styles.cardStats}>
          <span className={`${styles.statPill} ${styles.statSuccess}`}>
            {t('stats.success')}: {fileStats.success}
          </span>
          <span className={`${styles.statPill} ${styles.statFailure}`}>
            {t('stats.failure')}: {fileStats.failure}
          </span>
        </div>

        {/* 状态监测栏 */}
        {renderStatusBar(item)}

        <div className={styles.cardActions}>
          {showModelsButton && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => showModels(item)}
              className={styles.iconButton}
              title={t('auth_files.models_button', { defaultValue: '模型' })}
              disabled={disableControls}
            >
              <IconBot className={styles.actionIcon} size={16} />
            </Button>
          )}
          {!isRuntimeOnly && (
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => showDetails(item)}
                className={styles.iconButton}
                title={t('common.info', { defaultValue: '关于' })}
                disabled={disableControls}
              >
                <IconInfo className={styles.actionIcon} size={16} />
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => handleDownload(item.name)}
                className={styles.iconButton}
                title={t('auth_files.download_button')}
                disabled={disableControls}
              >
                <IconDownload className={styles.actionIcon} size={16} />
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void openPrefixProxyEditor(item.name)}
                className={styles.iconButton}
                title={t('auth_files.prefix_proxy_button')}
                disabled={disableControls}
              >
                <IconCode className={styles.actionIcon} size={16} />
              </Button>
              <Button
                variant="danger"
                size="sm"
                onClick={() => handleDelete(item.name)}
                className={styles.iconButton}
                title={t('auth_files.delete_button')}
                disabled={disableControls || deleting === item.name}
              >
                {deleting === item.name ? (
                  <LoadingSpinner size={14} />
                ) : (
                  <IconTrash2 className={styles.actionIcon} size={16} />
                )}
              </Button>
            </>
          )}
          {!isRuntimeOnly && (
            <div className={styles.statusToggle}>
              <ToggleSwitch
                ariaLabel={t('auth_files.status_toggle_label')}
                checked={!item.disabled}
                disabled={disableControls || statusUpdating[item.name] === true}
                onChange={(value) => void handleStatusToggle(item, value)}
              />
            </div>
          )}
          {isRuntimeOnly && (
            <div className={styles.virtualBadge}>
              {t('auth_files.type_virtual') || '虚拟认证文件'}
            </div>
          )}
        </div>
      </div>
    );
  };

  const titleNode = (
    <div className={styles.titleWrapper}>
      <span>{t('auth_files.title_section')}</span>
      {files.length > 0 && <span className={styles.countBadge}>{files.length}</span>}
    </div>
  );

  return (
    <div className={styles.container}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('auth_files.title')}</h1>
        <p className={styles.description}>{t('auth_files.description')}</p>
      </div>

      <Card
        title={titleNode}
        extra={
          <div className={styles.headerActions}>
            <Button variant="secondary" size="sm" onClick={handleHeaderRefresh} disabled={loading}>
              {t('common.refresh')}
            </Button>
            <Button
              size="sm"
              onClick={handleUploadClick}
              disabled={disableControls || uploading}
              loading={uploading}
            >
              {t('auth_files.upload_button')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              onClick={handleDeleteAll}
              disabled={disableControls || loading || deletingAll}
              loading={deletingAll}
            >
              {filter === 'all'
                ? t('auth_files.delete_all_button')
                : `${t('common.delete')} ${getTypeLabel(filter)}`}
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".json,application/json"
              multiple
              style={{ display: 'none' }}
              onChange={handleFileChange}
            />
          </div>
        }
      >
        {error && <div className={styles.errorBox}>{error}</div>}

        {/* 筛选区域 */}
        <div className={styles.filterSection}>
          {renderFilterTags()}

          <div className={styles.filterControls}>
            <div className={styles.filterItem}>
              <label>{t('auth_files.search_label')}</label>
              <Input
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(1);
                }}
                placeholder={t('auth_files.search_placeholder')}
              />
            </div>
            <div className={styles.filterItem}>
              <label>{t('auth_files.page_size_label')}</label>
              <input
                className={styles.pageSizeSelect}
                type="number"
                min={MIN_CARD_PAGE_SIZE}
                max={MAX_CARD_PAGE_SIZE}
                step={1}
                value={pageSizeInput}
                onChange={handlePageSizeChange}
                onBlur={(e) => commitPageSizeInput(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.currentTarget.blur();
                  }
                }}
              />
            </div>
          </div>
        </div>

        {/* 卡片网格 */}
        {loading ? (
          <div className={styles.hint}>{t('common.loading')}</div>
        ) : pageItems.length === 0 ? (
          <EmptyState
            title={t('auth_files.search_empty_title')}
            description={t('auth_files.search_empty_desc')}
          />
        ) : (
          <div className={styles.fileGrid}>{pageItems.map(renderFileCard)}</div>
        )}

        {/* 分页 */}
        {!loading && filtered.length > pageSize && (
          <div className={styles.pagination}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setPage(Math.max(1, currentPage - 1))}
              disabled={currentPage <= 1}
            >
              {t('auth_files.pagination_prev')}
            </Button>
            <div className={styles.pageInfo}>
              {t('auth_files.pagination_info', {
                current: currentPage,
                total: totalPages,
                count: filtered.length,
              })}
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
              disabled={currentPage >= totalPages}
            >
              {t('auth_files.pagination_next')}
            </Button>
          </div>
        )}
      </Card>

      {/* OAuth 排除列表卡片 */}
      <Card
        title={t('oauth_excluded.title')}
        extra={
          <Button
            size="sm"
            onClick={() => openExcludedEditor()}
            disabled={disableControls || excludedError === 'unsupported'}
          >
            {t('oauth_excluded.add')}
          </Button>
        }
      >
        {excludedError === 'unsupported' ? (
          <EmptyState
            title={t('oauth_excluded.upgrade_required_title')}
            description={t('oauth_excluded.upgrade_required_desc')}
          />
        ) : Object.keys(excluded).length === 0 ? (
          <EmptyState title={t('oauth_excluded.list_empty_all')} />
        ) : (
          <div className={styles.excludedList}>
            {Object.entries(excluded).map(([provider, models]) => (
              <div key={provider} className={styles.excludedItem}>
                <div className={styles.excludedInfo}>
                  <div className={styles.excludedProvider}>{provider}</div>
                  <div className={styles.excludedModels}>
                    {models?.length
                      ? t('oauth_excluded.model_count', { count: models.length })
                      : t('oauth_excluded.no_models')}
                  </div>
                </div>
                <div className={styles.excludedActions}>
                  <Button variant="secondary" size="sm" onClick={() => openExcludedEditor(provider)}>
                    {t('common.edit')}
                  </Button>
                  <Button variant="danger" size="sm" onClick={() => deleteExcluded(provider)}>
                    {t('oauth_excluded.delete')}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* OAuth 模型映射卡片 */}
      <Card
        title={t('oauth_model_alias.title')}
        extra={
          <Button
            size="sm"
            onClick={() => openModelAliasEditor()}
            disabled={disableControls || modelAliasError === 'unsupported'}
          >
            {t('oauth_model_alias.add')}
          </Button>
        }
      >
        {modelAliasError === 'unsupported' ? (
          <EmptyState
            title={t('oauth_model_alias.upgrade_required_title')}
            description={t('oauth_model_alias.upgrade_required_desc')}
          />
        ) : Object.keys(modelAlias).length === 0 ? (
          <EmptyState title={t('oauth_model_alias.list_empty_all')} />
        ) : (
          <div className={styles.excludedList}>
            {Object.entries(modelAlias).map(([provider, mappings]) => (
              <div key={provider} className={styles.excludedItem}>
                <div className={styles.excludedInfo}>
                  <div className={styles.excludedProvider}>{provider}</div>
                  <div className={styles.excludedModels}>
                    {mappings?.length
                      ? t('oauth_model_alias.model_count', { count: mappings.length })
                      : t('oauth_model_alias.no_models')}
                  </div>
                </div>
                <div className={styles.excludedActions}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => openModelAliasEditor(provider)}
                  >
                    {t('common.edit')}
                  </Button>
                  <Button variant="danger" size="sm" onClick={() => deleteModelAlias(provider)}>
                    {t('oauth_model_alias.delete')}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* 详情弹窗 */}
      <Modal
        open={detailModalOpen}
        onClose={() => setDetailModalOpen(false)}
        title={selectedFile?.name || t('auth_files.title_section')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDetailModalOpen(false)}>
              {t('common.close')}
            </Button>
            <Button
              onClick={() => {
                if (selectedFile) {
                  const text = JSON.stringify(selectedFile, null, 2);
                  navigator.clipboard.writeText(text).then(() => {
                    showNotification(t('notification.link_copied'), 'success');
                  });
                }
              }}
            >
              {t('common.copy')}
            </Button>
          </>
        }
      >
        {selectedFile && (
          <div className={styles.detailContent}>
            <pre className={styles.jsonContent}>{JSON.stringify(selectedFile, null, 2)}</pre>
          </div>
        )}
      </Modal>

      {/* 模型列表弹窗 */}
      <Modal
        open={modelsModalOpen}
        onClose={() => setModelsModalOpen(false)}
        title={
          t('auth_files.models_title', { defaultValue: '支持的模型' }) + ` - ${modelsFileName}`
        }
        footer={
          <Button variant="secondary" onClick={() => setModelsModalOpen(false)}>
            {t('common.close')}
          </Button>
        }
      >
        {modelsLoading ? (
          <div className={styles.hint}>
            {t('auth_files.models_loading', { defaultValue: '正在加载模型列表...' })}
          </div>
        ) : modelsError === 'unsupported' ? (
          <EmptyState
            title={t('auth_files.models_unsupported', { defaultValue: '当前版本不支持此功能' })}
            description={t('auth_files.models_unsupported_desc', {
              defaultValue: '请更新 CLI Proxy API 到最新版本后重试',
            })}
          />
        ) : modelsList.length === 0 ? (
          <EmptyState
            title={t('auth_files.models_empty', { defaultValue: '该凭证暂无可用模型' })}
            description={t('auth_files.models_empty_desc', {
              defaultValue: '该认证凭证可能尚未被服务器加载或没有绑定任何模型',
            })}
          />
        ) : (
          <div className={styles.modelsList}>
            {modelsList.map((model) => {
              const isExcluded = isModelExcluded(model.id, modelsFileType);
              return (
                <div
                  key={model.id}
                  className={`${styles.modelItem} ${isExcluded ? styles.modelItemExcluded : ''}`}
                  onClick={() => {
                    navigator.clipboard.writeText(model.id);
                    showNotification(
                      t('notification.link_copied', { defaultValue: '已复制到剪贴板' }),
                      'success'
                    );
                  }}
                  title={
                    isExcluded
                      ? t('auth_files.models_excluded_hint', {
                          defaultValue: '此模型已被 OAuth 排除',
                        })
                      : t('common.copy', { defaultValue: '点击复制' })
                  }
                >
                  <span className={styles.modelId}>{model.id}</span>
                  {model.display_name && model.display_name !== model.id && (
                    <span className={styles.modelDisplayName}>{model.display_name}</span>
                  )}
                  {model.type && <span className={styles.modelType}>{model.type}</span>}
                  {isExcluded && (
                    <span className={styles.modelExcludedBadge}>
                      {t('auth_files.models_excluded_badge', { defaultValue: '已排除' })}
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </Modal>

      {/* prefix/proxy_url 编辑弹窗 */}
      <Modal
        open={Boolean(prefixProxyEditor)}
        onClose={() => setPrefixProxyEditor(null)}
        closeDisabled={prefixProxyEditor?.saving === true}
        width={720}
        title={
          prefixProxyEditor?.fileName
            ? `${t('auth_files.prefix_proxy_button')} - ${prefixProxyEditor.fileName}`
            : t('auth_files.prefix_proxy_button')
        }
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => setPrefixProxyEditor(null)}
              disabled={prefixProxyEditor?.saving === true}
            >
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => void handlePrefixProxySave()}
              loading={prefixProxyEditor?.saving === true}
              disabled={
                disableControls ||
                prefixProxyEditor?.saving === true ||
                !prefixProxyDirty ||
                !prefixProxyEditor?.json
              }
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        {prefixProxyEditor && (
          <div className={styles.prefixProxyEditor}>
            {prefixProxyEditor.loading ? (
              <div className={styles.prefixProxyLoading}>
                <LoadingSpinner size={14} />
                <span>{t('auth_files.prefix_proxy_loading')}</span>
              </div>
            ) : (
              <>
                {prefixProxyEditor.error && (
                  <div className={styles.prefixProxyError}>{prefixProxyEditor.error}</div>
                )}
                <div className={styles.prefixProxyJsonWrapper}>
                  <label className={styles.prefixProxyLabel}>
                    {t('auth_files.prefix_proxy_source_label')}
                  </label>
                  <textarea
                    className={styles.prefixProxyTextarea}
                    rows={10}
                    readOnly
                    value={prefixProxyUpdatedText}
                  />
                </div>
                <div className={styles.prefixProxyFields}>
                  <Input
                    label={t('auth_files.prefix_label')}
                    value={prefixProxyEditor.prefix}
                    disabled={
                      disableControls || prefixProxyEditor.saving || !prefixProxyEditor.json
                    }
                    onChange={(e) => handlePrefixProxyChange('prefix', e.target.value)}
                  />
                  <Input
                    label={t('auth_files.proxy_url_label')}
                    value={prefixProxyEditor.proxyUrl}
                    placeholder={t('auth_files.proxy_url_placeholder')}
                    disabled={
                      disableControls || prefixProxyEditor.saving || !prefixProxyEditor.json
                    }
                    onChange={(e) => handlePrefixProxyChange('proxyUrl', e.target.value)}
                  />
                </div>
              </>
            )}
          </div>
        )}
      </Modal>

    </div>
  );
}
