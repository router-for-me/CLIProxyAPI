/**
 * Validation and type checking functions for quota management.
 */

import type { AuthFileItem } from '@/types';
import { GEMINI_CLI_IGNORED_MODEL_PREFIXES } from './constants';

export function resolveAuthProvider(file: AuthFileItem): string {
  const raw = file.provider ?? file.type ?? '';
  return String(raw).trim().toLowerCase();
}

export function isAntigravityFile(file: AuthFileItem): boolean {
  return resolveAuthProvider(file) === 'antigravity';
}

export function isClaudeFile(file: AuthFileItem): boolean {
  return resolveAuthProvider(file) === 'claude';
}

export function isClaudeOAuthFile(file: AuthFileItem): boolean {
  if (!isClaudeFile(file)) return false;
  const metadata =
    file && typeof file.metadata === 'object' && file.metadata !== null
      ? (file.metadata as Record<string, unknown>)
      : null;
  const accessToken =
    metadata && typeof metadata.access_token === 'string'
      ? metadata.access_token.trim()
      : '';
  return accessToken.includes('sk-ant-oat');
}

export function isCodexFile(file: AuthFileItem): boolean {
  return resolveAuthProvider(file) === 'codex';
}

export function isGeminiCliFile(file: AuthFileItem): boolean {
  return resolveAuthProvider(file) === 'gemini-cli';
}

export function isKimiFile(file: AuthFileItem): boolean {
  return resolveAuthProvider(file) === 'kimi';
}

export function isRuntimeOnlyAuthFile(file: AuthFileItem): boolean {
  const raw = file['runtime_only'] ?? file.runtimeOnly;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
}

export function isDisabledAuthFile(file: AuthFileItem): boolean {
  const raw = (file as { disabled?: unknown }).disabled;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'number') return raw !== 0;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
}

export function isIgnoredGeminiCliModel(modelId: string): boolean {
  return GEMINI_CLI_IGNORED_MODEL_PREFIXES.some(
    (prefix) => modelId === prefix || modelId.startsWith(`${prefix}-`)
  );
}
