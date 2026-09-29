/**
 * 凭证身份派生：卡片主行显示「账号」而不是文件名。
 * React-free —— 由 tests/authFileIdentity.test.ts 直接消费。
 *
 * 背景：真实文件名形如 codex-<hash8>-<email>-<plan>.json，email 在中段，
 * 单行尾部省略保留下来的恰好是类型徽章已表达过的 provider 前缀。
 *
 * 两条红线：
 * 1. 只读 email / projectId。后端还下发 account，但 api-key 类凭证的 account
 *    就是 API key 本身（sdk/cliproxy/auth/types.go AccountInfo），一旦进入主行、
 *    title 或搜索 haystack 就是密钥泄露；且 oauth 分支的 account 与 email 同源冗余。
 * 2. 不从文件名正则抽 email。codex 以 '-' 分隔，而 '-' 在 local part 与域名里都合法，
 *    codex-abc12345-first-last@example.com-team 无法被任何正则正确切分 —— 只会产出
 *    貌似真实的错值。需要 email 的提供商后端都有 json:"email"，无 email 的 kimi
 *    文件名里本来也没有 email 可抽。
 */

import type { AuthFileItem } from '@/types';

export type AuthFileIdentityKind = 'email' | 'projectId' | 'fileName';

export type AuthFileIdentity = {
  /** 卡片主行。无任何身份线索时为空串——不伪造占位符。 */
  primary: string;
  /** 主行来源。'fileName' 时主行用 mono 渲染，且副行不再重复。 */
  kind: AuthFileIdentityKind;
  /** 卡片副行（去掉 .json 的文件名）；null = 不渲染该行。 */
  secondary: string | null;
  /** 原始完整文件名，供副行 title 使用。 */
  fullName: string;
};

/** AuthFileItem 有索引签名，后端给非字符串也能通过类型检查——这里挡住。 */
const readIdentityText = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

/** 去掉 .json 后缀（大小写不敏感），仅在剥完仍有内容时生效。 */
export const stripJsonExtension = (name: string): string => {
  const trimmed = name.trim();
  if (trimmed.length <= 5) return trimmed;
  return trimmed.toLowerCase().endsWith('.json') ? trimmed.slice(0, -5) : trimmed;
};

/**
 * 身份回落链：email → projectId → 文件名（去 .json）。
 * 刻意与 provider 无关：runtime-only 虚拟凭证（name === email === 频道 ID）
 * 由副行去重守卫结构性处理，比按 provider 白名单更稳。
 */
export const deriveAuthFileIdentity = (file: AuthFileItem): AuthFileIdentity => {
  const fullName = readIdentityText(file.name);
  const base = stripJsonExtension(fullName);
  const email = readIdentityText(file.email);
  const projectId = readIdentityText(file.projectId);

  const kind: AuthFileIdentityKind = email ? 'email' : projectId ? 'projectId' : 'fileName';
  const primary = email || projectId || base;

  const secondary =
    kind === 'fileName' || !base || base.toLowerCase() === primary.toLowerCase() ? null : base;

  return { primary, kind, secondary, fullName };
};
