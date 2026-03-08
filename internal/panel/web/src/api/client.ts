/* ============================================================
 * HTTP 客户端 — 统一请求封装
 *
 * 职责:
 * 1. 自动注入 JWT Authorization 头
 * 2. 401 响应时尝试刷新 Token，失败则跳转登录
 * 3. 提供类型安全的 get / post / put / del 方法
 * ============================================================ */

const BASE_URL = window.location.origin + '/api/v1'
const TOKEN_KEY = 'community-access-token'
const REFRESH_TOKEN_KEY = 'community-refresh-token'

/* ----------------------------------------------------------
 * 通用响应 / 错误类型
 * ---------------------------------------------------------- */

export interface ApiResponse<T = unknown> {
  data: T
  status: number
  ok: boolean
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

/* ----------------------------------------------------------
 * Token 读写（直接操作 localStorage，与 store 解耦）
 * ---------------------------------------------------------- */

export function getAccessToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setAccessToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function getRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_TOKEN_KEY)
}

export function setRefreshToken(token: string): void {
  localStorage.setItem(REFRESH_TOKEN_KEY, token)
}

export function clearTokens(): void {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(REFRESH_TOKEN_KEY)
}

/* ----------------------------------------------------------
 * Token 刷新锁 — 保证并发请求只刷新一次
 * ---------------------------------------------------------- */

let refreshPromise: Promise<boolean> | null = null

async function tryRefreshToken(): Promise<boolean> {
  if (refreshPromise) return refreshPromise

  refreshPromise = (async () => {
    const token = getRefreshToken()
    if (!token) return false

    try {
      const res = await fetch(`${BASE_URL}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: token }),
      })
      if (!res.ok) return false

      const data = await res.json()
      setAccessToken(data.access_token)
      setRefreshToken(data.refresh_token)
      return true
    } catch {
      return false
    } finally {
      refreshPromise = null
    }
  })()

  return refreshPromise
}

/* ----------------------------------------------------------
 * 核心请求函数
 * ---------------------------------------------------------- */

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  isRetry = false,
): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }

  const accessToken = getAccessToken()
  if (accessToken) {
    headers['Authorization'] = `Bearer ${accessToken}`
  }

  const config: RequestInit = { method, headers }
  if (body !== undefined) {
    config.body = JSON.stringify(body)
  }

  const res = await fetch(`${BASE_URL}${path}`, config)

  // 401 处理：尝试刷新 Token 后重试一次
  if (res.status === 401 && !isRetry) {
    const refreshed = await tryRefreshToken()
    if (refreshed) {
      return request<T>(method, path, body, true)
    }
    // 刷新失败 -> 清除凭证并跳转登录
    clearTokens()
    window.location.href = '/panel/login'
    throw new ApiError(401, 'Session expired')
  }

  // 非 2xx 响应统一抛错
  if (!res.ok) {
    let errorMsg = `请求失败 (${res.status})`
    try {
      const errBody = await res.json()
      if (errBody.error) errorMsg = errBody.error
      if (errBody.message) errorMsg = errBody.message
    } catch {
      // 无法解析 JSON，使用默认错误信息
    }
    throw new ApiError(res.status, errorMsg)
  }

  // 204 No Content
  if (res.status === 204) {
    return { data: null as T, status: 204, ok: true }
  }

  const data = (await res.json()) as T
  return { data, status: res.status, ok: true }
}

/* ----------------------------------------------------------
 * 快捷方法
 * ---------------------------------------------------------- */

export function get<T>(path: string): Promise<ApiResponse<T>> {
  return request<T>('GET', path)
}

export function post<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>('POST', path, body)
}

export function put<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>('PUT', path, body)
}

export function del<T>(path: string): Promise<ApiResponse<T>> {
  return request<T>('DELETE', path)
}
