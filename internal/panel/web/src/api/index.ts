/* ============================================================
 * API 模块统一导出
 * ============================================================ */

export * as authApi from './auth'
export * as userApi from './user'
export * as adminApi from './admin'
export { ApiError, get, post, put, del } from './client'
export type { ApiResponse } from './client'
