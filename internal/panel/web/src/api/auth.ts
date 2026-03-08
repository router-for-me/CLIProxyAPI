/* ============================================================
 * 认证 API — 登录 / 注册 / Token 刷新 / 邮箱验证
 * 与后端 handler_auth.go 路由对齐
 * ============================================================ */

import { post, type ApiResponse } from './client'

/* ----------------------------------------------------------
 * 类型定义 — 与后端 db.User / TokenPair 对齐
 * ---------------------------------------------------------- */

export interface User {
  id: number
  uuid: string
  username: string
  email: string
  role: 'user' | 'admin'
  status: 'active' | 'banned'
  api_key: string
  invite_code: string
  pool_mode: 'public' | 'private' | 'hybrid'
  oauth_provider: string
  created_at: string
  updated_at: string
}

export interface TokenPair {
  access_token: string
  refresh_token: string
  expires_in: number
}

export interface LoginResponse {
  user: User
  tokens: TokenPair
}

export interface RegisterData {
  username: string
  email?: string
  password: string
  invite_code?: string
  email_code?: string
}

/* ----------------------------------------------------------
 * API 调用
 * ---------------------------------------------------------- */

/**
 * 用户登录
 * POST /api/v1/auth/login
 * @param loginId 用户名或邮箱
 * @param password 密码
 */
export function login(
  loginId: string,
  password: string,
): Promise<ApiResponse<LoginResponse>> {
  return post<LoginResponse>('/auth/login', { login: loginId, password })
}

/**
 * 用户注册
 * POST /api/v1/auth/register
 */
export function register(
  data: RegisterData,
): Promise<ApiResponse<LoginResponse>> {
  return post<LoginResponse>('/auth/register', data)
}

/**
 * 刷新 Token
 * POST /api/v1/auth/refresh
 */
export function refreshToken(
  refreshTokenValue: string,
): Promise<ApiResponse<TokenPair>> {
  return post<TokenPair>('/auth/refresh', {
    refresh_token: refreshTokenValue,
  })
}

/**
 * 发送邮箱验证码
 * POST /api/v1/auth/send-code
 */
export function sendVerificationCode(
  email: string,
): Promise<ApiResponse<{ message: string }>> {
  return post<{ message: string }>('/auth/send-code', { email })
}
