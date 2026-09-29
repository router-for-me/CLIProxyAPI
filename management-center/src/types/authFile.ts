/**
 * 认证文件相关类型
 * 基于原项目 src/modules/auth-files.js
 */

import type { RecentRequestBucket } from '@/utils/recentRequests';

export type AuthFileType =
  | 'qwen'
  | 'kimi'
  | 'gemini'
  | 'aistudio'
  | 'claude'
  | 'codex'
  | 'devin'
  | 'meta'
  | 'antigravity'
  | 'xai'
  | 'iflow'
  | 'vertex'
  | 'empty'
  | 'unknown';

export interface AuthFileCooldown {
  scope: 'model' | 'credential';
  modelKey?: string;
  reason: string;
  retryAt: string;
  remainingSeconds: number;
  backoffLevel?: number;
  httpStatus?: number;
}

export interface AuthFileCooldownSnapshot {
  /** Server observation time, not the start of the cooldown. */
  observedAt?: string;
  /** Local receipt time anchors relative timers without relying on synchronized clocks. */
  receivedAtMs: number;
  /** null = runtime state unknown; [] = known, with no active timers. */
  records: AuthFileCooldown[] | null;
}

export interface AuthFileItem {
  name: string;
  type?: AuthFileType | string;
  provider?: string;
  /**
   * 凭证账号邮箱（后端 auth_files 两条分支都会填：磁盘扫描读 JSON 的 email 字段，
   * 注册表读 Metadata/Attributes）。卡片主行用它领衔。
   * 注意：后端还会下发 account/account_type，但 api-key 类凭证的 account 就是
   * API key 本身（AccountInfo() → return "api_key", apiKey），**绝不可用于展示或搜索**。
   */
  email?: string;
  /** GCP / Vertex 项目 ID，账号邮箱缺失时作为身份回落。 */
  projectId?: string;
  size?: number;
  authIndex?: string | number | null;
  runtimeOnly?: boolean | string;
  disabled?: boolean;
  unavailable?: boolean;
  status?: string;
  statusMessage?: string;
  lastRefresh?: string | number;
  modified?: number;
  priority?: number;
  weight?: number;
  note?: string;
  success?: unknown;
  failed?: unknown;
  /** 归一化后的累计成功/失败计数（由 API 边界从 success/failed 生字段填充）。 */
  successCount?: number;
  failureCount?: number;
  recent_requests?: RecentRequestBucket[];
  recentRequests?: RecentRequestBucket[];
  /** Absent on older servers. Never interpreted as credential health. */
  cooldownSnapshot?: AuthFileCooldownSnapshot;
  [key: string]: unknown;
}

export interface AuthFilesResponse {
  files: AuthFileItem[];
  total?: number;
  observed_at?: unknown;
  observedAt?: string;
}
