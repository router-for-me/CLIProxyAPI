/**
 * 安全存储服务
 * 基于原项目 src/utils/secure-storage.js
 */

import { encryptData, decryptData } from '@/utils/encryption';

interface StorageOptions {
  encrypt?: boolean;
}

class SecureStorageService {
  /**
   * 存储数据
   */
  setItem(key: string, value: any, options: StorageOptions = {}): void {
    const { encrypt = true } = options;

    if (value === null || value === undefined) {
      this.removeItem(key);
      return;
    }

    const stringValue = JSON.stringify(value);
    const storedValue = encrypt ? encryptData(stringValue) : stringValue;

    localStorage.setItem(key, storedValue);
  }

  /**
   * 获取数据
   */
  getItem<T = any>(key: string, options: StorageOptions = {}): T | null {
    const { encrypt = true } = options;

    const raw = localStorage.getItem(key);
    if (raw === null) return null;

    try {
      const decrypted = encrypt ? decryptData(raw) : raw;
      return JSON.parse(decrypted) as T;
    } catch {
      // JSON解析失败,尝试兼容旧的纯字符串数据 (非JSON格式)
      try {
        // 如果是加密的,尝试解密后直接返回
        if (encrypt && raw.startsWith('enc::v1::')) {
          const decrypted = decryptData(raw);
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

      let parsed: any = raw;
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

export const secureStorage = new SecureStorageService();
