const STORAGE_KEY = "mgmt-key";

class APIError extends Error {
  status: number;
  body: unknown;

  constructor(status: number, body: unknown) {
    const message =
      typeof body === "object" && body !== null && "error" in body
        ? String((body as { error: unknown }).error)
        : `HTTP ${status}`;
    super(message);
    this.name = "APIError";
    this.status = status;
    this.body = body;
  }
}

function getBaseURL(): string {
  if (typeof window !== "undefined") {
    return window.location.origin;
  }
  return "";
}

function getManagementKey(): string {
  if (typeof window === "undefined") return "";
  return localStorage.getItem(STORAGE_KEY) ?? "";
}

export function setManagementKey(key: string): void {
  localStorage.setItem(STORAGE_KEY, key);
}

export function clearManagementKey(): void {
  localStorage.removeItem(STORAGE_KEY);
}

export function hasManagementKey(): boolean {
  if (typeof window === "undefined") return false;
  return localStorage.getItem(STORAGE_KEY) !== null;
}

async function request<T>(
  path: string,
  options: RequestInit = {}
): Promise<T> {
  const base = getBaseURL();
  const key = getManagementKey();
  const url = `${base}/v0/management${path}`;

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(options.headers as Record<string, string> | undefined),
  };

  if (key) {
    headers["Authorization"] = `Bearer ${key}`;
  }

  const res = await fetch(url, {
    ...options,
    headers,
  });

  if (!res.ok) {
    let body: unknown;
    try {
      body = await res.json();
    } catch {
      body = await res.text();
    }
    throw new APIError(res.status, body);
  }

  const contentType = res.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    return res.json() as Promise<T>;
  }
  if (contentType.includes("text/") || contentType.includes("yaml")) {
    return res.text() as unknown as T;
  }
  return res.blob() as unknown as T;
}

function get<T>(path: string): Promise<T> {
  return request<T>(path, { method: "GET" });
}

function put<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "PUT",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

function patch<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "PATCH",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

function del<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "DELETE",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "POST",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

async function getBool(path: string): Promise<boolean> {
  const res = await get<Record<string, boolean>>(path);
  const values = Object.values(res);
  return values[0] ?? false;
}

async function getInt(path: string): Promise<number> {
  const res = await get<Record<string, number>>(path);
  const values = Object.values(res);
  return values[0] ?? 0;
}

async function getStr(path: string): Promise<string> {
  const res = await get<Record<string, string>>(path);
  const values = Object.values(res);
  return values[0] ?? "";
}

export interface ConfigResponse {
  [key: string]: unknown;
}

export interface BooleanValue {
  value: boolean;
}

export interface IntValue {
  value: number;
}

export interface StringValue {
  value: string;
}

export interface ModelAlias {
  name: string;
  alias: string;
}

export interface GeminiKey {
  "api-key": string;
  priority?: number;
  prefix?: string;
  "base-url"?: string;
  "proxy-url"?: string;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  "excluded-models"?: string[];
}

export interface ClaudeKey {
  "api-key": string;
  priority?: number;
  prefix?: string;
  "base-url"?: string;
  "proxy-url"?: string;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  "excluded-models"?: string[];
}

export interface CodexKey {
  "api-key": string;
  priority?: number;
  prefix?: string;
  "base-url"?: string;
  websockets?: boolean;
  "proxy-url"?: string;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  "excluded-models"?: string[];
}

export interface VertexKey {
  "api-key": string;
  priority?: number;
  prefix?: string;
  "base-url"?: string;
  "proxy-url"?: string;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  "excluded-models"?: string[];
}

export interface OpenAICompatAPIKeyEntry {
  "api-key": string;
  "proxy-url"?: string;
}

export interface OpenAICompatEntry {
  name: string;
  priority?: number;
  prefix?: string;
  "base-url": string;
  "api-key-entries"?: OpenAICompatAPIKeyEntry[];
  models?: ModelAlias[];
  headers?: Record<string, string>;
}

export interface AmpModelMapping {
  from: string;
  to: string;
}

export interface AmpUpstreamAPIKeyEntry {
  "upstream-api-key": string;
  "api-keys": string[];
}

export interface OAuthExcludedModel {
  model: string;
  reason?: string;
}

export interface OAuthModelAlias {
  alias: string;
  model: string;
}

export interface OAuthProvider {
  key: string;
  display_name: string;
  flow_type: string;
  auth_url_endpoint: string;
  aliases?: string[];
  configured?: boolean;
}

export interface AuthFile {
  name: string;
  provider: string;
  status: string;
  models?: string[];
  size?: number;
  modified?: string;
  disabled?: boolean;
  fields?: Record<string, string>;
}

export interface AuthFileModels {
  models: string[];
}

export interface UsageStatistics {
  [key: string]: unknown;
}

export interface ApplicationLogs {
  lines: string[];
  line_count: number;
  latest_timestamp: number;
}

export interface ErrorLogFile {
  name: string;
  size: number;
  modified: string;
}

export interface ErrorLogFiles {
  files: ErrorLogFile[];
}

export interface RequestLogEntry {
  id: string;
  [key: string]: unknown;
}

export interface AmpCodeConfig {
  "upstream-url": string;
  "upstream-api-key": string;
  "upstream-api-keys"?: AmpUpstreamAPIKeyEntry[];
  "restrict-management-to-localhost": boolean;
  "model-mappings"?: AmpModelMapping[];
  "force-model-mappings": boolean;
}

export interface ModelMapping {
  source: string;
  target: string;
}

export interface ModelDefinitions {
  [channel: string]: unknown;
}

export interface CopilotQuota {
  quota: number;
  used: number;
  remaining: number;
}

export interface AuthStatus {
  status: "ok" | "wait" | "error" | "device_code" | "auth_url";
  error?: string;
  url?: string;
  user_code?: string;
  verification_uri?: string;
  message?: string;
}

export interface LatestVersion {
  "latest-version": string;
}

export interface RoutingStrategy {
  value: string;
}

export const api = {
  config: {
    getConfig: () => get<ConfigResponse>("/config"),
    getConfigYAML: () => get<string>("/config.yaml"),
    putConfigYAML: (yaml: string) =>
      put<{ status: string }>("/config.yaml", { yaml }),
    getLatestVersion: () => get<LatestVersion>("/latest-version"),
  },

  boolean: {
    getDebug: () => getBool("/debug"),
    putDebug: (value: boolean) => put<unknown>("/debug", { value }),
    getLoggingToFile: () => getBool("/logging-to-file"),
    putLoggingToFile: (value: boolean) =>
      put<unknown>("/logging-to-file", { value }),
    getUsageStatisticsEnabled: () => getBool("/usage-statistics-enabled"),
    putUsageStatisticsEnabled: (value: boolean) =>
      put<unknown>("/usage-statistics-enabled", { value }),
    getRequestLogEnabled: () => getBool("/request-log"),
    putRequestLog: (value: boolean) =>
      put<unknown>("/request-log", { value }),
    getWsAuth: () => getBool("/ws-auth"),
    putWsAuth: (value: boolean) => put<unknown>("/ws-auth", { value }),
    getForceModelPrefix: () => getBool("/force-model-prefix"),
    putForceModelPrefix: (value: boolean) =>
      put<unknown>("/force-model-prefix", { value }),
  },

  string: {
    getProxyURL: () => getStr("/proxy-url"),
    putProxyURL: (value: string) => put<unknown>("/proxy-url", { value }),
    deleteProxyURL: () => del<unknown>("/proxy-url"),
  },

  integer: {
    getLogsMaxTotalSizeMB: () => getInt("/logs-max-total-size-mb"),
    putLogsMaxTotalSizeMB: (value: number) =>
      put<unknown>("/logs-max-total-size-mb", { value }),
    getErrorLogsMaxFiles: () => getInt("/error-logs-max-files"),
    putErrorLogsMaxFiles: (value: number) =>
      put<unknown>("/error-logs-max-files", { value }),
    getRequestRetry: () => getInt("/request-retry"),
    putRequestRetry: (value: number) =>
      put<unknown>("/request-retry", { value }),
    getMaxRetryInterval: () => getInt("/max-retry-interval"),
    putMaxRetryInterval: (value: number) =>
      put<unknown>("/max-retry-interval", { value }),
  },

  routing: {
    getRoutingStrategy: async () => {
      const res = await get<{ strategy: string }>("/routing/strategy");
      return res.strategy ?? "round-robin";
    },
    putRoutingStrategy: (value: string) =>
      put<unknown>("/routing/strategy", { value }),
  },

  apiKeys: {
    getAPIKeys: async () => {
      const res = await get<{ "api-keys": string[] }>("/api-keys");
      return res["api-keys"] ?? [];
    },
    putAPIKeys: (keys: string[]) => put<unknown>("/api-keys", keys),
    patchAPIKeys: (body: { old?: string; new?: string; index?: number; value?: string }) =>
      patch<unknown>("/api-keys", body),
    deleteAPIKeys: (params: { index?: number; value?: string }) => {
      const query = new URLSearchParams();
      if (params.index !== undefined) query.set("index", String(params.index));
      if (params.value) query.set("value", params.value);
      return del<{ status: string }>(`/api-keys?${query.toString()}`);
    },
  },

  geminiKeys: {
    getGeminiKeys: async () => {
      const res = await get<{ "gemini-api-key": GeminiKey[] }>("/gemini-api-key");
      return res["gemini-api-key"] ?? [];
    },
    putGeminiKeys: (keys: GeminiKey[]) => put<unknown>("/gemini-api-key", keys),
    patchGeminiKey: (body: { index?: number; match?: string; value: Partial<GeminiKey> }) =>
      patch<unknown>("/gemini-api-key", body),
    deleteGeminiKey: (params: { index?: number; "api-key"?: string; "base-url"?: string }) => {
      const query = new URLSearchParams();
      if (params.index !== undefined) query.set("index", String(params.index));
      if (params["api-key"]) query.set("api-key", params["api-key"]);
      if (params["base-url"]) query.set("base-url", params["base-url"]);
      return del<{ status: string }>(`/gemini-api-key?${query.toString()}`);
    },
  },

  claudeKeys: {
    getClaudeKeys: async () => {
      const res = await get<{ "claude-api-key": ClaudeKey[] }>("/claude-api-key");
      return res["claude-api-key"] ?? [];
    },
    putClaudeKeys: (keys: ClaudeKey[]) => put<unknown>("/claude-api-key", keys),
    patchClaudeKey: (body: { index?: number; match?: string; value: Partial<ClaudeKey> }) =>
      patch<unknown>("/claude-api-key", body),
    deleteClaudeKey: (params: { index?: number; "api-key"?: string; "base-url"?: string }) => {
      const query = new URLSearchParams();
      if (params.index !== undefined) query.set("index", String(params.index));
      if (params["api-key"]) query.set("api-key", params["api-key"]);
      if (params["base-url"]) query.set("base-url", params["base-url"]);
      return del<{ status: string }>(`/claude-api-key?${query.toString()}`);
    },
  },

  codexKeys: {
    getCodexKeys: async () => {
      const res = await get<{ "codex-api-key": CodexKey[] }>("/codex-api-key");
      return res["codex-api-key"] ?? [];
    },
    putCodexKeys: (keys: CodexKey[]) => put<unknown>("/codex-api-key", keys),
    patchCodexKey: (body: { index?: number; match?: string; value: Partial<CodexKey> }) =>
      patch<unknown>("/codex-api-key", body),
    deleteCodexKey: (params: { index?: number; "api-key"?: string; "base-url"?: string }) => {
      const query = new URLSearchParams();
      if (params.index !== undefined) query.set("index", String(params.index));
      if (params["api-key"]) query.set("api-key", params["api-key"]);
      if (params["base-url"]) query.set("base-url", params["base-url"]);
      return del<{ status: string }>(`/codex-api-key?${query.toString()}`);
    },
  },

  vertexKeys: {
    getVertexKeys: async () => {
      const res = await get<{ "vertex-api-key": VertexKey[] }>("/vertex-api-key");
      return res["vertex-api-key"] ?? [];
    },
    putVertexKeys: (keys: VertexKey[]) => put<unknown>("/vertex-api-key", keys),
    patchVertexKey: (body: { index?: number; match?: string; value: Partial<VertexKey> }) =>
      patch<unknown>("/vertex-api-key", body),
    deleteVertexKey: (params: { index?: number; "api-key"?: string; "base-url"?: string }) => {
      const query = new URLSearchParams();
      if (params.index !== undefined) query.set("index", String(params.index));
      if (params["api-key"]) query.set("api-key", params["api-key"]);
      if (params["base-url"]) query.set("base-url", params["base-url"]);
      return del<{ status: string }>(`/vertex-api-key?${query.toString()}`);
    },
  },

  openAICompat: {
    getOpenAICompat: async () => {
      const res = await get<{ "openai-compatibility": OpenAICompatEntry[] }>("/openai-compatibility");
      return res["openai-compatibility"] ?? [];
    },
    putOpenAICompat: (entries: OpenAICompatEntry[]) =>
      put<unknown>("/openai-compatibility", entries),
    patchOpenAICompat: (body: { name?: string; index?: number; value: Partial<OpenAICompatEntry> }) =>
      patch<unknown>("/openai-compatibility", body),
    deleteOpenAICompat: (params: { name?: string; index?: number }) => {
      const query = new URLSearchParams();
      if (params.name) query.set("name", params.name);
      if (params.index !== undefined) query.set("index", String(params.index));
      return del<{ status: string }>(`/openai-compatibility?${query.toString()}`);
    },
  },

  oauth: {
    getOAuthExcludedModels: async () => {
      const res = await get<{ "oauth-excluded-models": Record<string, string[]> }>("/oauth-excluded-models");
      return res["oauth-excluded-models"] ?? {};
    },
    putOAuthExcludedModels: (models: Record<string, string[]>) =>
      put<unknown>("/oauth-excluded-models", models),
    patchOAuthExcludedModels: (body: { provider: string; models: string[] }) =>
      patch<unknown>("/oauth-excluded-models", body),
    deleteOAuthExcludedModels: (params: { provider: string }) => {
      const query = new URLSearchParams(params).toString();
      return del<unknown>(`/oauth-excluded-models?${query.toString()}`);
    },
    getOAuthModelAlias: async () => {
      const res = await get<{ "oauth-model-alias": Record<string, ModelAlias[]> }>("/oauth-model-alias");
      return res["oauth-model-alias"] ?? {};
    },
    putOAuthModelAlias: (aliases: Record<string, ModelAlias[]>) =>
      put<unknown>("/oauth-model-alias", aliases),
    patchOAuthModelAlias: (body: { provider: string; aliases: ModelAlias[] }) =>
      patch<unknown>("/oauth-model-alias", body),
    deleteOAuthModelAlias: (params: { channel: string }) => {
      const query = new URLSearchParams(params).toString();
      return del<unknown>(`/oauth-model-alias?${query.toString()}`);
    },
    getOAuthProviders: async () => {
      const res = await get<{ providers: OAuthProvider[] }>("/oauth-providers");
      return res.providers ?? [];
    },
  },

  authFiles: {
    listAuthFiles: async () => {
      const res = await get<{ files: AuthFile[] }>("/auth-files");
      return res.files ?? [];
    },
    getAuthFileModels: () => get<AuthFileModels>("/auth-files/models"),
    downloadAuthFile: (params: { name: string; provider: string }) => {
      const query = new URLSearchParams(params).toString();
      return get<Blob>(`/auth-files/download?${query}`);
    },
    uploadAuthFile: (formData: FormData) => {
      const base = getBaseURL();
      const key = getManagementKey();
      const url = `${base}/v0/management/auth-files`;
      const headers: Record<string, string> = {};
      if (key) {
        headers["Authorization"] = `Bearer ${key}`;
      }
      return fetch(url, {
        method: "POST",
        headers,
        body: formData,
      }).then(async (res) => {
        if (!res.ok) {
          let body: unknown;
          try {
            body = await res.json();
          } catch {
            body = await res.text();
          }
          throw new APIError(res.status, body);
        }
        return res.json();
      });
    },
    deleteAuthFile: (params: { name: string; provider: string }) =>
      del<{ status: string }>("/auth-files", params),
    patchAuthFileStatus: (params: {
      name: string;
      provider: string;
      disabled: boolean;
    }) => patch<{ status: string }>("/auth-files/status", params),
    patchAuthFileFields: (params: {
      name: string;
      provider: string;
      fields: Record<string, string>;
    }) => patch<{ status: string }>("/auth-files/fields", params),
  },

  oauthFlows: {
    requestAnthropicToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/anthropic-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestCodexToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/codex-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestGeminiCLIToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/gemini-cli-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestGitLabToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/gitlab-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestAntigravityToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/antigravity-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestKimiToken: () =>
      get<{ status: string; url?: string; state?: string; user_code?: string; verification_uri?: string }>("/kimi-auth-url"),
    requestIFlowToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/iflow-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestKiroToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string; method?: string; user_code?: string; verification_uri?: string }>(`/kiro-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestCursorToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/cursor-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    requestGitHubToken: () =>
      get<{ status: string; url?: string; state?: string; user_code?: string; verification_uri?: string }>("/github-auth-url"),
    requestKiloToken: () =>
      get<{ status: string; url?: string; state?: string; user_code?: string; verification_uri?: string }>("/kilo-auth-url"),
    requestQoderToken: (params?: Record<string, unknown>) =>
      get<{ status: string; url?: string; state?: string }>(`/qoder-auth-url${params ? '?' + new URLSearchParams(params as Record<string, string>).toString() : ''}`),
    postOAuthCallback: (body: Record<string, unknown>) =>
      post<{ status: string }>("/oauth-callback", body),
    getAuthStatus: (state: string) =>
      get<AuthStatus>(`/get-auth-status?state=${encodeURIComponent(state)}`),
  },

  usage: {
    getUsageStatistics: async () => {
      const res = await get<UsageStatistics>("/usage");
      return res;
    },
    exportUsageStatistics: async () => {
      const res = await get<UsageStatistics>("/usage/export");
      return res;
    },
    importUsageStatistics: (data: unknown) =>
      post<{ added: number; skipped: number; total_requests: number; failed_requests: number }>("/usage/import", data),
  },

  logs: {
    getLogs: (after?: number) =>
      get<ApplicationLogs>(`/logs${after != null ? `?after=${after}` : ""}`),
    deleteLogs: () => del<{ status: string }>("/logs"),
    getRequestErrorLogs: () => get<ErrorLogFiles>("/request-error-logs"),
    downloadRequestErrorLog: (name: string) =>
      get<Blob>(`/request-error-logs/${encodeURIComponent(name)}`),
    getRequestLogByID: (id: string) =>
      get<RequestLogEntry>(`/request-log-by-id/${encodeURIComponent(id)}`),
    getRequestLogEnabled: () => getBool("/request-log"),
    putRequestLog: (value: boolean) =>
      put<unknown>("/request-log", { value }),
  },

  ampCode: {
    getAmpCode: async () => {
      const res = await get<{ ampcode: AmpCodeConfig }>("/ampcode");
      return res.ampcode;
    },
    getAmpUpstreamURL: async () => {
      const res = await get<{ "upstream-url": string }>("/ampcode/upstream-url");
      return res["upstream-url"] ?? "";
    },
    putAmpUpstreamURL: (value: string) =>
      put<unknown>("/ampcode/upstream-url", { value }),
    deleteAmpUpstreamURL: () =>
      del<{ status: string }>("/ampcode/upstream-url"),
    getAmpUpstreamAPIKey: async () => {
      const res = await get<{ "upstream-api-key": string }>("/ampcode/upstream-api-key");
      return res["upstream-api-key"] ?? "";
    },
    putAmpUpstreamAPIKey: (value: string) =>
      put<unknown>("/ampcode/upstream-api-key", { value }),
    deleteAmpUpstreamAPIKey: () =>
      del<{ status: string }>("/ampcode/upstream-api-key"),
    getAmpRestrictManagementToLocalhost: async () => {
      const res = await get<{ "restrict-management-to-localhost": boolean }>("/ampcode/restrict-management-to-localhost");
      return res["restrict-management-to-localhost"] ?? false;
    },
    putAmpRestrictManagementToLocalhost: (value: boolean) =>
      put<unknown>("/ampcode/restrict-management-to-localhost", { value }),
    getAmpForceModelMappings: async () => {
      const res = await get<{ "force-model-mappings": boolean }>("/ampcode/force-model-mappings");
      return res["force-model-mappings"] ?? false;
    },
    putAmpForceModelMappings: (value: boolean) =>
      put<unknown>("/ampcode/force-model-mappings", { value }),
    getAmpModelMappings: async () => {
      const res = await get<{ "model-mappings": AmpModelMapping[] }>("/ampcode/model-mappings");
      return res["model-mappings"] ?? [];
    },
    putAmpModelMappings: (mappings: AmpModelMapping[]) =>
      put<unknown>("/ampcode/model-mappings", { value: mappings }),
    patchAmpModelMappings: (mappings: AmpModelMapping[]) =>
      patch<unknown>("/ampcode/model-mappings", { value: mappings }),
    deleteAmpModelMappings: (fromKeys: string[]) =>
      del<{ status: string }>("/ampcode/model-mappings", { value: fromKeys }),
    getAmpUpstreamAPIKeys: async () => {
      const res = await get<{ "upstream-api-keys": AmpUpstreamAPIKeyEntry[] }>("/ampcode/upstream-api-keys");
      return res["upstream-api-keys"] ?? [];
    },
    putAmpUpstreamAPIKeys: (keys: AmpUpstreamAPIKeyEntry[]) =>
      put<unknown>("/ampcode/upstream-api-keys", { value: keys }),
    patchAmpUpstreamAPIKeys: (keys: AmpUpstreamAPIKeyEntry[]) =>
      patch<unknown>("/ampcode/upstream-api-keys", { value: keys }),
    deleteAmpUpstreamAPIKeys: (upstreamKeys: string[]) =>
      del<{ status: string }>("/ampcode/upstream-api-keys", { value: upstreamKeys }),
  },

  quota: {
    getSwitchProject: () => get<BooleanValue>("/quota-exceeded/switch-project"),
    putSwitchProject: (value: boolean) =>
      put<BooleanValue>("/quota-exceeded/switch-project", { value }),
    getSwitchPreviewModel: () => get<BooleanValue>("/quota-exceeded/switch-preview-model"),
    putSwitchPreviewModel: (value: boolean) =>
      put<BooleanValue>("/quota-exceeded/switch-preview-model", { value }),
    getCopilotQuota: () => get<CopilotQuota>("/copilot-quota"),
  },

  modelDefinitions: {
    getModelDefinitions: (channel: string) =>
      get<ModelDefinitions>(`/model-definitions/${encodeURIComponent(channel)}`),
  },

  apiCall: (body: Record<string, unknown>) =>
    post<Record<string, unknown>>("/api-call", body),

  vertexImport: (body: { project_id: string; private_key: string; client_email: string; [key: string]: unknown }) =>
    post<{ status: string }>("/vertex/import", body),
};

export { APIError };
