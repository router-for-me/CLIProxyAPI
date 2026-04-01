/**
 * OAuth 相关类型
 * 基于原项目 src/modules/oauth.js
 */

// OAuth 提供商类型
export type OAuthProvider =
  | 'codex'
  | 'anthropic'
  | 'antigravity'
  | 'gemini-cli'
  | 'kimi'
  | 'qwen';

// OAuth 流程状态
export interface OAuthFlow {
  provider: OAuthProvider;
  deviceCode: string;
  userCode: string;
  verificationUrl: string;
  expiresAt: Date;
  interval: number;
  status: 'pending' | 'authorized' | 'expired' | 'error';
}

// OAuth 配置
export interface OAuthConfig {
  clientId?: string;
  clientSecret?: string;
  redirectUri?: string;
}

// OAuth 排除模型列表
export interface OAuthExcludedModels {
  models: string[];
}

// OAuth 模型别名
export interface OAuthModelAliasEntry {
  name: string;
  alias: string;
  fork?: boolean;
}

export type OAuthModelAlias = Record<string, OAuthModelAliasEntry[]>;
