/**
 * 辅助工具函数
 * 从原项目 src/utils/array.js, dom.js, html.js 迁移
 */

/**
 * 规范化数组响应（处理后端可能返回非数组的情况）
 */
export function normalizeArrayResponse<T>(data: T | T[] | null | undefined): T[] {
  if (!data) return [];
  if (Array.isArray(data)) return data;
  return [data];
}

/**
 * 防抖函数
 */
export function debounce<This, Args extends unknown[], Return>(
  func: (this: This, ...args: Args) => Return,
  delay: number
): (this: This, ...args: Args) => void {
  let timeoutId: ReturnType<typeof setTimeout>;

  return function (this: This, ...args: Args) {
    clearTimeout(timeoutId);
    timeoutId = setTimeout(() => func.apply(this, args), delay);
  };
}

/**
 * 节流函数
 */
export function throttle<This, Args extends unknown[], Return>(
  func: (this: This, ...args: Args) => Return,
  limit: number
): (this: This, ...args: Args) => void {
  let inThrottle: boolean;

  return function (this: This, ...args: Args) {
    if (!inThrottle) {
      func.apply(this, args);
      inThrottle = true;
      setTimeout(() => (inThrottle = false), limit);
    }
  };
}

/**
 * HTML 转义（防 XSS）
 */
export function escapeHtml(text: string): string {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

/**
 * 生成唯一 ID
 */
export function generateId(): string {
  return `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
}

/**
 * 深拷贝对象
 */
export function deepClone<T>(obj: T): T {
  if (obj === null || typeof obj !== 'object') return obj;

  if (obj instanceof Date) return new Date(obj.getTime()) as unknown as T;
  if (Array.isArray(obj)) return obj.map((item) => deepClone(item)) as unknown as T;

  const source = obj as Record<string, unknown>;
  const cloned: Record<string, unknown> = {};
  for (const key in source) {
    if (Object.prototype.hasOwnProperty.call(source, key)) {
      cloned[key] = deepClone(source[key]);
    }
  }
  return cloned as unknown as T;
}

/**
 * 延迟函数
 */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
