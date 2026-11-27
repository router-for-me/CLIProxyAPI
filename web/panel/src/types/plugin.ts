export type PluginConfigFieldType =
  | 'string'
  | 'number'
  | 'integer'
  | 'boolean'
  | 'enum'
  | 'array'
  | 'object';

export interface PluginConfigField {
  name: string;
  type: PluginConfigFieldType | string;
  enumValues: string[];
  description: string;
}

export type PluginConfigObject = Record<string, unknown>;

export interface PluginMetadata {
  name: string;
  version: string;
  author: string;
  githubRepository: string;
  logo: string;
  configFields: PluginConfigField[];
}

export interface PluginMenu {
  path: string;
  menu: string;
  description: string;
}

/** 插件为某个登录入口单独发布的展示信息。 */
export interface PluginOAuthProviderEntry {
  identifier: string;
  name?: string;
  logo?: string;
}

export interface PluginListEntry {
  id: string;
  path: string;
  configured: boolean;
  registered: boolean;
  enabled: boolean;
  effectiveEnabled: boolean;
  supportsOAuth: boolean;
  oauthProvider?: string;
  /** 声明的全部登录入口；旧后端只发单个 `oauth_provider` 时为空。 */
  oauthProviders?: string[];
  oauthProviderEntries?: PluginOAuthProviderEntry[];
  supportsQuota: boolean;
  quotaProvider?: string;
  logo: string;
  configFields: PluginConfigField[];
  menus: PluginMenu[];
  metadata: PluginMetadata | null;
}

export interface PluginListResponse {
  pluginsEnabled: boolean;
  pluginsDir: string;
  plugins: PluginListEntry[];
}

export interface PluginDeleteResult {
  status: string;
  id: string;
  path: string;
  fileDeleted: boolean;
  configuredRemoved: boolean;
  restartRequired: boolean;
}

export interface PluginStoreEntry {
  storeId: string;
  sourceId: string;
  sourceName: string;
  sourceUrl: string;
  id: string;
  name: string;
  description: string;
  author: string;
  version: string;
  repository: string;
  installType: string;
  authRequired: boolean;
  authConfigured: boolean;
  platforms: PluginStorePlatform[];
  logo: string;
  homepage: string;
  license: string;
  tags: string[];
  /** 后端已把键归一化为 BCP-47 形式（如 `zh-CN`），无匹配语言时为空。 */
  locales?: Record<string, PluginStoreLocale>;
  installed: boolean;
  installedVersion: string;
  path: string;
  configured: boolean;
  registered: boolean;
  enabled: boolean;
  effectiveEnabled: boolean;
  updateAvailable: boolean;
}

export interface PluginStorePlatform {
  goos: string;
  goarch: string;
}

/** 插件商店条目的单语言展示信息；字段均可缺省，缺失时逐字段回退到根字段。 */
export interface PluginStoreLocale {
  name?: string;
  description?: string;
  author?: string;
  homepage?: string;
  license?: string;
  tags?: string[];
}

export interface PluginStoreSourceError {
  sourceId: string;
  sourceName: string;
  sourceUrl: string;
  message: string;
}

export interface PluginStoreResponse {
  pluginsEnabled: boolean;
  pluginsDir: string;
  sourceErrors: PluginStoreSourceError[];
  plugins: PluginStoreEntry[];
}

export interface PluginStoreInstallResult {
  status: string;
  sourceId: string;
  sourceName: string;
  sourceUrl: string;
  id: string;
  version: string;
  installType: string;
  path: string;
  pluginsEnabled: boolean;
  restartRequired: boolean;
}
