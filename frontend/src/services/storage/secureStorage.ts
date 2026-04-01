/**
 * 本地存储混淆服务（可逆）
 * 基于原项目 src/utils/secure-storage.js
 *
 * IMPORTANT: 这不是安全边界，仅用于避免“肉眼直读”的轻度混淆。
 */

import { obfuscateData, deobfuscateData, isObfuscated } from '@/utils/encryption';

interface StorageOptions {
  /**
   * Whether to obfuscate the stored value. This was historically called `encrypt`,
   * but the implementation is reversible obfuscation, not cryptographic security.
   */
  obfuscate?: boolean;
  encrypt?: boolean;
}

class ObfuscatedStorageService {
  /**
   * 存储数据
   */
  setItem(key: string, value: unknown, options: StorageOptions = {}): void {
    const obfuscate = options.obfuscate ?? options.encrypt ?? true;

    if (value === null || value === undefined) {
      this.removeItem(key);
      return;
    }

    const stringValue = JSON.stringify(value);
    const storedValue = obfuscate ? obfuscateData(stringValue) : stringValue;

    localStorage.setItem(key, storedValue);
  }

  /**
   * 获取数据
   */
  getItem<T = unknown>(key: string, options: StorageOptions = {}): T | null {
    const obfuscate = options.obfuscate ?? options.encrypt ?? true;

    const raw = localStorage.getItem(key);
    if (raw === null) return null;

    try {
      const decrypted = obfuscate ? deobfuscateData(raw) : raw;
      return JSON.parse(decrypted) as T;
    } catch {
      // JSON解析失败,尝试兼容旧的纯字符串数据 (非JSON格式)
      try {
        // 如果是加密的,尝试解密后直接返回
        if (obfuscate && isObfuscated(raw)) {
          const decrypted = deobfuscateData(raw);
          // 解密后如果还不是JSON,返回原始字符串
          return decrypted as T;
        }
        // 非加密的纯字符串,直接返回
        return raw as T;
      } catch {
        // 完全失败,静默返回null (避免控制台污染)
        return null;
      }
    }
  }

  /**
   * 删除数据
   */
  removeItem(key: string): void {
    localStorage.removeItem(key);
  }

  /**
   * 清空所有数据
   */
  clear(): void {
    localStorage.clear();
  }

  /**
   * 迁移旧的明文缓存为加密格式
   */
  migratePlaintextKeys(keys: string[]): void {
    keys.forEach((key) => {
      const raw = localStorage.getItem(key);
      if (!raw) return;

      // 如果已经是加密格式，跳过
      if (raw.startsWith('enc::v1::')) {
        return;
      }

      let parsed: unknown = raw;
      try {
        parsed = JSON.parse(raw);
      } catch {
        // 原值不是 JSON，直接使用字符串
        parsed = raw;
      }

      try {
        this.setItem(key, parsed);
      } catch (error) {
        console.warn(`Failed to migrate key "${key}":`, error);
      }
    });
  }

  /**
   * 检查键是否存在
   */
  hasItem(key: string): boolean {
    return localStorage.getItem(key) !== null;
  }
}

export const obfuscatedStorage = new ObfuscatedStorageService();
// Backward-compatible alias (historically named "secureStorage").
export const secureStorage = obfuscatedStorage;
