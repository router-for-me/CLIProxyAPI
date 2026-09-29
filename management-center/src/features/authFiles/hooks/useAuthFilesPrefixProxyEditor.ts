import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi, type AuthFileFieldsPatch } from '@/services/api';
import type { AuthFileItem } from '@/types';
import { useNotificationStore } from '@/stores';
import {
  applyAuthFileWebsockets,
  applyAuthFileUsingApi,
  normalizeProviderKey,
  parsePriorityValue,
  readAuthFileDisableCooling,
  readAuthFileWebsockets,
  readAuthFileUsingApi,
  supportsAuthFileWebsockets,
  supportsAuthFileUsingApi,
} from '@/features/authFiles/constants';
import {
  parseCredentialWeightText,
  readCredentialWeight,
  validateCredentialWeightText,
  type CredentialWeightError,
} from '@/utils/credentialWeight';

type AuthFileHeaders = Record<string, string>;
type AuthFileHeadersErrorKey =
  | 'auth_files.headers_invalid_json'
  | 'auth_files.headers_invalid_object'
  | 'auth_files.headers_invalid_value';
type AuthFileContentErrorKey =
  'auth_files.prefix_proxy_invalid_json' | 'auth_files.prefix_proxy_html_challenge';
type AuthFileWeightErrorKey = 'auth_files.weight_invalid_integer' | 'auth_files.weight_invalid_max';
type AuthFileEditorErrorKey = AuthFileHeadersErrorKey | AuthFileWeightErrorKey;

export type PrefixProxyEditorField =
  | 'prefix'
  | 'proxyUrl'
  | 'priority'
  | 'weight'
  | 'disableCooling'
  | 'websockets'
  | 'usingApi'
  | 'note'
  | 'excludedModelsText'
  | 'headersText';

export type PrefixProxyEditorFieldValue = string | boolean;

export type PrefixProxyEditorState = {
  fileName: string;
  fileInfoText: string;
  loading: boolean;
  saving: boolean;
  error: string | null;
  originalText: string;
  rawText: string;
  invalidContentPreview: string;
  json: Record<string, unknown> | null;
  providerKey: string;
  prefix: string;
  proxyUrl: string;
  priority: string;
  weight: string;
  weightError: string | null;
  disableCooling: boolean;
  disableCoolingTouched: boolean;
  websockets: boolean;
  websocketsTouched: boolean;
  usingApi: boolean;
  usingApiTouched: boolean;
  note: string;
  noteTouched: boolean;
  excludedModelsText: string;
  excludedModelsTouched: boolean;
  headersText: string;
  headersTouched: boolean;
  headersError: string | null;
};

export type UseAuthFilesPrefixProxyEditorOptions = {
  disableControls: boolean;
  loadFiles: () => Promise<void>;
};

export type UseAuthFilesPrefixProxyEditorResult = {
  prefixProxyEditor: PrefixProxyEditorState | null;
  prefixProxyUpdatedText: string;
  prefixProxyDirty: boolean;
  openPrefixProxyEditor: (file: AuthFileItem) => Promise<void>;
  closePrefixProxyEditor: () => void;
  handlePrefixProxyChange: (
    field: PrefixProxyEditorField,
    value: PrefixProxyEditorFieldValue
  ) => void;
  handlePrefixProxySave: () => Promise<void>;
};

const isRecordObject = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const validateHeadersValue = (value: unknown): AuthFileHeadersErrorKey | null => {
  if (!isRecordObject(value)) {
    return 'auth_files.headers_invalid_object';
  }
  return Object.values(value).every((item) => typeof item === 'string')
    ? null
    : 'auth_files.headers_invalid_value';
};

const parseHeadersText = (
  text: string
): { value: AuthFileHeaders | null; errorKey: AuthFileHeadersErrorKey | null } => {
  const trimmed = text.trim();
  if (!trimmed) {
    return { value: null, errorKey: null };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text) as unknown;
  } catch {
    return { value: null, errorKey: 'auth_files.headers_invalid_json' };
  }

  const errorKey = validateHeadersValue(parsed);
  if (errorKey) {
    return { value: null, errorKey };
  }

  return { value: parsed as AuthFileHeaders, errorKey: null };
};

const credentialWeightErrorKey = (error: CredentialWeightError): AuthFileWeightErrorKey =>
  error === 'max' ? 'auth_files.weight_invalid_max' : 'auth_files.weight_invalid_integer';

const normalizeTextField = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

const normalizeExcludedModels = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: string[] = [];
  value.forEach((item) => {
    if (typeof item !== 'string') return;
    const model = item.trim();
    const key = model.toLowerCase();
    if (!model || seen.has(key)) return;
    seen.add(key);
    result.push(model);
  });
  return result;
};

const parseExcludedModelsText = (value: string): string[] =>
  normalizeExcludedModels(value.split(/\r?\n/));

const readExcludedModels = (value: Record<string, unknown>): string[] =>
  normalizeExcludedModels(
    value.excluded_models !== undefined ? value.excluded_models : value['excluded-models']
  );

const getExcludedModelsField = (
  value: Record<string, unknown>
): 'excluded_models' | 'excluded-models' =>
  value.excluded_models === undefined && value['excluded-models'] !== undefined
    ? 'excluded-models'
    : 'excluded_models';

const INVALID_CONTENT_PREVIEW_LIMIT = 1000;

const buildInvalidContentPreview = (text: string): string => {
  const trimmed = text.trim();
  if (!trimmed) return '';
  if (trimmed.length <= INVALID_CONTENT_PREVIEW_LIMIT) return trimmed;
  return `${trimmed.slice(0, INVALID_CONTENT_PREVIEW_LIMIT)}\n...`;
};

const buildInvalidAuthFileContentState = (
  text: string,
  resolveError: (key: AuthFileContentErrorKey) => string
): Pick<
  PrefixProxyEditorState,
  'loading' | 'error' | 'rawText' | 'originalText' | 'invalidContentPreview'
> => ({
  loading: false,
  error: resolveError(getAuthFileContentErrorKey(text)),
  rawText: text,
  originalText: text,
  invalidContentPreview: buildInvalidContentPreview(text),
});

const getAuthFileContentErrorKey = (text: string): AuthFileContentErrorKey => {
  const head = text.trimStart().slice(0, 4096).toLowerCase();
  const looksLikeHtml =
    head.startsWith('<!doctype html') ||
    head.startsWith('<html') ||
    head.includes('<head') ||
    head.includes('<body');
  const looksLikeChallenge =
    head.includes('cf_chl') ||
    head.includes('__cf_chl_tk') ||
    head.includes('challenge-platform') ||
    head.includes('cloudflare');

  return looksLikeHtml || looksLikeChallenge
    ? 'auth_files.prefix_proxy_html_challenge'
    : 'auth_files.prefix_proxy_invalid_json';
};

const hasKeys = (value: Record<string, unknown> | AuthFileFieldsPatch | null): boolean =>
  Boolean(value && Object.keys(value).length > 0);

const normalizeHeaders = (value: unknown): AuthFileHeaders => {
  if (!isRecordObject(value)) return {};

  return Object.entries(value).reduce<AuthFileHeaders>((result, [key, rawValue]) => {
    if (typeof rawValue !== 'string') return result;
    const name = key.trim();
    const headerValue = rawValue.trim();
    if (!name || !headerValue) return result;
    result[name] = headerValue;
    return result;
  }, {});
};

const buildHeadersPatch = (
  originalHeaders: AuthFileHeaders,
  nextHeaders: AuthFileHeaders
): AuthFileHeaders | undefined => {
  const patch: AuthFileHeaders = {};
  const nextNames = new Set(Object.keys(nextHeaders));

  Object.entries(nextHeaders).forEach(([name, value]) => {
    if (originalHeaders[name] !== value) {
      patch[name] = value;
    }
  });

  Object.keys(originalHeaders).forEach((name) => {
    if (!nextNames.has(name)) {
      patch[name] = '';
    }
  });

  return Object.keys(patch).length > 0 ? patch : undefined;
};

const applyHeadersPatch = (
  value: Record<string, unknown>,
  headersPatch: AuthFileHeaders | undefined
) => {
  if (!headersPatch) return;

  const nextHeaders = normalizeHeaders(value.headers);
  Object.entries(headersPatch).forEach(([name, rawValue]) => {
    const headerName = name.trim();
    if (!headerName) return;
    const headerValue = rawValue.trim();
    if (!headerValue) {
      delete nextHeaders[headerName];
      return;
    }
    nextHeaders[headerName] = headerValue;
  });

  if (Object.keys(nextHeaders).length > 0) {
    value.headers = nextHeaders;
  } else {
    delete value.headers;
  }
};

export const buildAuthFileFieldsPatch = (
  editor: PrefixProxyEditorState,
  resolveError: (key: AuthFileEditorErrorKey) => string
): AuthFileFieldsPatch => {
  const original = editor.json ?? {};
  const patch: AuthFileFieldsPatch = {};

  const originalPrefix = normalizeTextField(original.prefix);
  const nextPrefix = editor.prefix.trim();
  if (nextPrefix !== originalPrefix) {
    patch.prefix = nextPrefix;
  }

  const originalProxyURL = normalizeTextField(original.proxy_url);
  const nextProxyURL = editor.proxyUrl.trim();
  if (nextProxyURL !== originalProxyURL) {
    patch.proxy_url = nextProxyURL;
  }

  const originalPriority = parsePriorityValue(original.priority);
  const priorityText = editor.priority.trim();
  const nextPriority = parsePriorityValue(priorityText);
  if (!priorityText) {
    if (originalPriority !== undefined && originalPriority !== 0) {
      patch.priority = 0;
    }
  } else if (nextPriority !== undefined) {
    if (nextPriority === 0) {
      if (originalPriority !== undefined && originalPriority !== 0) {
        patch.priority = 0;
      }
    } else if (nextPriority !== originalPriority) {
      patch.priority = nextPriority;
    }
  }

  const weightError = validateCredentialWeightText(editor.weight);
  if (weightError) {
    throw new Error(resolveError(credentialWeightErrorKey(weightError)));
  }
  const originalWeight = readCredentialWeight(original.weight);
  const nextWeight = parseCredentialWeightText(editor.weight);
  if (nextWeight === undefined) {
    if (originalWeight !== undefined) patch.weight = null;
  } else if (nextWeight !== originalWeight) {
    patch.weight = nextWeight;
  }

  if (editor.disableCoolingTouched) {
    const originalDisableCooling = readAuthFileDisableCooling(original);
    const nextDisableCooling = Boolean(editor.disableCooling);
    if (nextDisableCooling !== originalDisableCooling) {
      const field =
        original.disable_cooling === undefined && original['disable-cooling'] !== undefined
          ? 'disable-cooling'
          : 'disable_cooling';
      patch[field] = nextDisableCooling;
    }
  }

  if (editor.noteTouched) {
    const originalNote = normalizeTextField(original.note);
    const nextNote = editor.note.trim();
    if (nextNote !== originalNote) {
      patch.note = nextNote;
    }
  }

  if (editor.excludedModelsTouched) {
    const originalExcludedModels = readExcludedModels(original);
    const nextExcludedModels = parseExcludedModelsText(editor.excludedModelsText);
    if (JSON.stringify(nextExcludedModels) !== JSON.stringify(originalExcludedModels)) {
      patch[getExcludedModelsField(original)] = nextExcludedModels;
    }
  }

  if (supportsAuthFileWebsockets(editor.providerKey) && editor.websocketsTouched) {
    const originalWebsockets = readAuthFileWebsockets(original);
    const nextWebsockets = Boolean(editor.websockets);
    if (nextWebsockets !== originalWebsockets) {
      patch.websockets = nextWebsockets;
    }
  }

  if (supportsAuthFileUsingApi(editor.providerKey) && editor.usingApiTouched) {
    const originalUsingApi = readAuthFileUsingApi(original);
    const nextUsingApi = Boolean(editor.usingApi);
    if (nextUsingApi !== originalUsingApi) {
      patch.using_api = nextUsingApi;
    }
  }

  if (editor.headersTouched) {
    const { value: parsedHeaders, errorKey } = parseHeadersText(editor.headersText);
    if (errorKey) {
      throw new Error(resolveError(errorKey));
    }
    const headersPatch = buildHeadersPatch(
      normalizeHeaders(original.headers),
      normalizeHeaders(parsedHeaders ?? {})
    );
    if (headersPatch) {
      patch.headers = headersPatch;
    }
  }

  return patch;
};

const buildPrefixProxyUpdatedText = (
  editor: PrefixProxyEditorState | null,
  resolveError: (key: AuthFileEditorErrorKey) => string
): string => {
  if (!editor?.json) return editor?.rawText ?? '';
  const patch = buildAuthFileFieldsPatch(editor, resolveError);
  let next: Record<string, unknown> = { ...editor.json };
  if (patch.prefix !== undefined) {
    if (patch.prefix) {
      next.prefix = patch.prefix;
    } else {
      delete next.prefix;
    }
  }
  if (patch.proxy_url !== undefined) {
    if (patch.proxy_url) {
      next.proxy_url = patch.proxy_url;
    } else {
      delete next.proxy_url;
    }
  }

  if (patch.priority !== undefined) {
    if (patch.priority === 0) {
      delete next.priority;
    } else {
      next.priority = patch.priority;
    }
  }

  if (patch.weight !== undefined) {
    if (patch.weight === null) {
      delete next.weight;
    } else {
      next.weight = patch.weight;
    }
  }

  if (patch.disable_cooling !== undefined) {
    next.disable_cooling = patch.disable_cooling;
  }
  if (patch['disable-cooling'] !== undefined) {
    next['disable-cooling'] = patch['disable-cooling'];
  }

  if (patch.note !== undefined) {
    if (patch.note) {
      next.note = patch.note;
    } else if ('note' in next) {
      delete next.note;
    }
  }

  if (patch.excluded_models !== undefined) {
    next.excluded_models = patch.excluded_models;
  }
  if (patch['excluded-models'] !== undefined) {
    next['excluded-models'] = patch['excluded-models'];
  }

  applyHeadersPatch(next, patch.headers);

  if (patch.websockets !== undefined) {
    next = applyAuthFileWebsockets(next, patch.websockets);
  }

  if (patch.using_api !== undefined) {
    next = applyAuthFileUsingApi(next, patch.using_api);
  }

  return JSON.stringify(next);
};

export function useAuthFilesPrefixProxyEditor(
  options: UseAuthFilesPrefixProxyEditorOptions
): UseAuthFilesPrefixProxyEditorResult {
  const { disableControls, loadFiles } = options;
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [prefixProxyEditor, setPrefixProxyEditor] = useState<PrefixProxyEditorState | null>(null);

  const hasBlockingValidationError = Boolean(
    (prefixProxyEditor?.headersTouched && prefixProxyEditor.headersError) ||
    prefixProxyEditor?.weightError
  );
  const prefixProxyUpdatedText =
    prefixProxyEditor && !hasBlockingValidationError
      ? buildPrefixProxyUpdatedText(prefixProxyEditor, (key) => t(key))
      : '';

  const prefixProxyPatch =
    prefixProxyEditor?.json && !hasBlockingValidationError
      ? buildAuthFileFieldsPatch(prefixProxyEditor, (key) => t(key))
      : null;

  const prefixProxyDirty = hasKeys(prefixProxyPatch);

  const closePrefixProxyEditor = () => {
    setPrefixProxyEditor(null);
  };

  const openPrefixProxyEditor = async (file: AuthFileItem) => {
    const name = file.name;
    const fileProviderKey = normalizeProviderKey(String(file.type ?? file.provider ?? ''));

    if (disableControls) return;
    if (prefixProxyEditor?.fileName === name) {
      setPrefixProxyEditor(null);
      return;
    }

    setPrefixProxyEditor({
      fileName: name,
      fileInfoText: JSON.stringify(file, null, 2),
      loading: true,
      saving: false,
      error: null,
      originalText: '',
      rawText: '',
      invalidContentPreview: '',
      json: null,
      providerKey: fileProviderKey,
      prefix: '',
      proxyUrl: '',
      priority: '',
      weight: '',
      weightError: null,
      disableCooling: false,
      disableCoolingTouched: false,
      websockets: false,
      websocketsTouched: false,
      usingApi: false,
      usingApiTouched: false,
      note: '',
      noteTouched: false,
      excludedModelsText: '',
      excludedModelsTouched: false,
      headersText: '',
      headersTouched: false,
      headersError: null,
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
            ...buildInvalidAuthFileContentState(rawText, (key) => t(key)),
          };
        });
        return;
      }

      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setPrefixProxyEditor((prev) => {
          if (!prev || prev.fileName !== name) return prev;
          return {
            ...prev,
            ...buildInvalidAuthFileContentState(rawText, (key) => t(key)),
          };
        });
        return;
      }

      const json = { ...(parsed as Record<string, unknown>) };
      const originalText = JSON.stringify(json);
      const providerKey = normalizeProviderKey(
        String(json.type ?? json.provider ?? file.type ?? file.provider ?? '')
      );
      const prefix = typeof json.prefix === 'string' ? json.prefix : '';
      const proxyUrl = typeof json.proxy_url === 'string' ? json.proxy_url : '';
      const priority = parsePriorityValue(json.priority);
      const weight = readCredentialWeight(json.weight);
      const disableCooling = readAuthFileDisableCooling(json);
      const websockets = supportsAuthFileWebsockets(providerKey)
        ? readAuthFileWebsockets(json)
        : false;
      const usingApi = supportsAuthFileUsingApi(providerKey) ? readAuthFileUsingApi(json) : false;
      const note = typeof json.note === 'string' ? json.note : '';
      const excludedModelsText = readExcludedModels(json).join('\n');
      const headers = json.headers;
      let headersText = '';
      let headersError: string | null = null;
      if (headers !== undefined) {
        headersText = JSON.stringify(headers, null, 2);
        const { errorKey } = parseHeadersText(headersText);
        headersError = errorKey ? t(errorKey) : null;
      }

      setPrefixProxyEditor((prev) => {
        if (!prev || prev.fileName !== name) return prev;
        return {
          ...prev,
          loading: false,
          originalText,
          rawText: originalText,
          invalidContentPreview: '',
          json,
          providerKey,
          prefix,
          proxyUrl,
          priority: priority !== undefined ? String(priority) : '',
          weight: weight !== undefined ? String(weight) : '',
          weightError: null,
          disableCooling,
          disableCoolingTouched: false,
          websockets,
          websocketsTouched: false,
          usingApi,
          usingApiTouched: false,
          note,
          noteTouched: false,
          excludedModelsText,
          excludedModelsTouched: false,
          headersText,
          headersTouched: false,
          headersError,
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

  const handlePrefixProxyChange = (
    field: PrefixProxyEditorField,
    value: PrefixProxyEditorFieldValue
  ) => {
    setPrefixProxyEditor((prev) => {
      if (!prev) return prev;
      if (field === 'prefix') return { ...prev, prefix: String(value) };
      if (field === 'proxyUrl') return { ...prev, proxyUrl: String(value) };
      if (field === 'priority') return { ...prev, priority: String(value) };
      if (field === 'weight') {
        const weight = String(value);
        const error = validateCredentialWeightText(weight);
        return {
          ...prev,
          weight,
          weightError: error ? t(credentialWeightErrorKey(error)) : null,
        };
      }
      if (field === 'disableCooling') {
        return {
          ...prev,
          disableCooling: Boolean(value),
          disableCoolingTouched: true,
        };
      }
      if (field === 'websockets') {
        return { ...prev, websockets: Boolean(value), websocketsTouched: true };
      }
      if (field === 'usingApi') {
        return { ...prev, usingApi: Boolean(value), usingApiTouched: true };
      }
      if (field === 'note') return { ...prev, note: String(value), noteTouched: true };
      if (field === 'excludedModelsText') {
        return {
          ...prev,
          excludedModelsText: String(value),
          excludedModelsTouched: true,
        };
      }
      if (field === 'headersText') {
        const headersText = String(value);
        const { errorKey } = parseHeadersText(headersText);
        return {
          ...prev,
          headersText,
          headersTouched: true,
          headersError: errorKey ? t(errorKey) : null,
        };
      }
      return prev;
    });
  };

  const handlePrefixProxySave = async () => {
    if (!prefixProxyEditor?.json) return;
    if (!prefixProxyDirty) return;

    const name = prefixProxyEditor.fileName;
    let payload: AuthFileFieldsPatch;
    try {
      payload = buildAuthFileFieldsPatch(prefixProxyEditor, (key) => t(key));
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : 'Invalid format';
      showNotification(errorMessage, 'error');
      return;
    }
    if (!hasKeys(payload)) return;

    setPrefixProxyEditor((prev) => {
      if (!prev || prev.fileName !== name) return prev;
      return { ...prev, saving: true };
    });

    try {
      await authFilesApi.patchFields(name, payload);
      showNotification(t('auth_files.prefix_proxy_saved_success', { name }), 'success');
      await loadFiles();
      setPrefixProxyEditor(null);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.update_failed')}: ${errorMessage}`, 'error');
      setPrefixProxyEditor((prev) => {
        if (!prev || prev.fileName !== name) return prev;
        return { ...prev, saving: false };
      });
    }
  };

  return {
    prefixProxyEditor,
    prefixProxyUpdatedText,
    prefixProxyDirty,
    openPrefixProxyEditor,
    closePrefixProxyEditor,
    handlePrefixProxyChange,
    handlePrefixProxySave,
  };
}
