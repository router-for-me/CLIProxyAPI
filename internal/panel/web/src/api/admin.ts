/* ============================================================
 * 管理端 API — 用户管理 / 全局统计 / 数据导出
 * 与后端 handler_admin.go 路由对齐
 *
 * 注意: 此文件同时保留了完整的管理端类型定义，
 * 供 admin 页面组件直接导入使用。
 * ============================================================ */

import { get, post, put, del, type ApiResponse } from './client'
import type { User } from './auth'

/* ----------------------------------------------------------
 * 通用类型
 * ---------------------------------------------------------- */

export type Role = 'admin' | 'user'
export type UserStatus = 'active' | 'banned' | 'pending'
export type PoolMode = 'private' | 'public' | 'contributor'
export type QuotaType = 'count' | 'token' | 'both'
export type QuotaPeriod = 'daily' | 'weekly' | 'monthly' | 'total'
export type HealthStatus = 'healthy' | 'degraded' | 'down'

/* ----------------------------------------------------------
 * 数据模型
 * ---------------------------------------------------------- */

export interface ExportResponse {
  url: string
  filename: string
}

export interface QuotaConfig {
  id: number
  model_pattern: string
  quota_type: QuotaType
  max_requests: number
  request_period: QuotaPeriod
  max_tokens: number
  token_period: QuotaPeriod
  created_at: string
  updated_at: string
}

export interface Credential {
  id: string
  provider: string
  owner_id: number | null
  health: HealthStatus
  weight: number
  enabled: boolean
  added_at: string
  success_rate?: number
  last_check?: string
  owner_name?: string
}

export interface RedemptionCode {
  id: number
  code: string
  template_id: number
  template_name: string
  used_by: number | null
  used_at: string | null
  expires_at: string | null
  created_at: string
}

export interface InviteCode {
  id: number
  code: string
  type: 'admin_created' | 'user_referral'
  creator_id: number
  creator_name: string
  max_uses: number
  used_count: number
  expires_at: string | null
  status: 'active' | 'exhausted' | 'expired' | 'disabled'
  created_at: string
}

export interface AuditLogEntry {
  id: number
  user_id: number | null
  username: string
  action: string
  target: string
  detail: string
  ip: string
  created_at: string
}

export interface SecurityModuleStatus {
  ip_control: boolean
  rate_limit: boolean
  anomaly_detection: boolean
  audit: boolean
}

export interface IPRule {
  id: number
  cidr: string
  rule_type: 'whitelist' | 'blacklist'
  description: string
  created_at: string
}

export interface SMTPConfig {
  host: string
  port: number
  username: string
  password: string
  from: string
  use_tls: boolean
}

export interface OAuthProvider {
  id: number
  name: string
  provider: string
  client_id: string
  client_secret: string
  enabled: boolean
}

export type RoutingStrategy = 'WeightedRoundRobin' | 'LeastLoad' | 'FillFirst' | 'Random'

export interface CredentialMetrics {
  credential_id: number
  provider: string
  total_requests: number
  success_rate: number
  avg_latency_ms: number
  weight: number
  circuit_state: 'closed' | 'open' | 'half_open'
}

export interface HealthCheckerSettings {
  check_interval_sec: number
  failure_threshold: number
  recovery_timeout_sec: number
}

export interface RouterConfig {
  strategy: RoutingStrategy
  credentials: CredentialMetrics[]
  health: HealthCheckerSettings
}

export interface PaginatedResponse<T> {
  data: T[]
  total: number
  page: number
  page_size: number
}

/* ----------------------------------------------------------
 * 用户管理 API
 * ---------------------------------------------------------- */

export function banUser(id: number): Promise<ApiResponse<{ message: string }>> {
  return post<{ message: string }>(`/admin/users/${id}/ban`)
}

export function unbanUser(id: number): Promise<ApiResponse<{ message: string }>> {
  return post<{ message: string }>(`/admin/users/${id}/unban`)
}

export function updateUserRole(
  userId: number,
  role: Role,
): Promise<ApiResponse<void>> {
  return put<void>(`/admin/users/${userId}/role`, { role })
}

/* ----------------------------------------------------------
 * 额度配置 API
 * ---------------------------------------------------------- */

export function createQuotaConfig(
  cfg: Omit<QuotaConfig, 'id' | 'created_at' | 'updated_at'>,
): Promise<ApiResponse<QuotaConfig>> {
  return post<QuotaConfig>('/admin/quota-configs', cfg)
}

export function updateQuotaConfig(
  id: number,
  cfg: Partial<QuotaConfig>,
): Promise<ApiResponse<QuotaConfig>> {
  return put<QuotaConfig>(`/admin/quota-configs/${id}`, cfg)
}

export function deleteQuotaConfig(id: number): Promise<ApiResponse<void>> {
  return del<void>(`/admin/quota-configs/${id}`)
}

/* ----------------------------------------------------------
 * 凭证池 API
 * ---------------------------------------------------------- */

export function removeCredential(id: string): Promise<ApiResponse<void>> {
  return del<void>(`/admin/credentials/${id}`)
}

export function bulkHealthCheck(): Promise<ApiResponse<{ checked: number }>> {
  return post<{ checked: number }>('/admin/credentials/health-check')
}

/* ----------------------------------------------------------
 * 兑换码 API
 * ---------------------------------------------------------- */

export function generateCodes(data: {
  template_id: number
  count: number
  expires_at?: string
}): Promise<ApiResponse<{ codes: string[] }>> {
  return post<{ codes: string[] }>('/admin/redemption/generate', data)
}

/* ----------------------------------------------------------
 * 邀请码 API
 * ---------------------------------------------------------- */

export function getInviteCodes(): Promise<ApiResponse<InviteCode[]>> {
  return get<InviteCode[]>('/admin/invites')
}

export function createInviteCode(params: {
  max_uses: number
  bonus_quota?: number
  expires_at?: string
}): Promise<ApiResponse<InviteCode>> {
  return post<InviteCode>('/admin/invites', params)
}

export function disableInviteCode(id: number): Promise<ApiResponse<void>> {
  return put<void>(`/admin/invites/${id}/disable`)
}

/* ----------------------------------------------------------
 * 安全中心 API
 * ---------------------------------------------------------- */

export function getSecurityStatus(): Promise<ApiResponse<SecurityModuleStatus>> {
  return get<SecurityModuleStatus>('/admin/security/status')
}

export function toggleSecurityModule(
  module: keyof SecurityModuleStatus,
  enabled: boolean,
): Promise<ApiResponse<void>> {
  return put<void>('/admin/security/toggle', { module, enabled })
}

export function getIPRules(): Promise<ApiResponse<IPRule[]>> {
  return get<IPRule[]>('/admin/security/ip-rules')
}

export function createIPRule(params: {
  cidr: string
  rule_type: 'whitelist' | 'blacklist'
  description: string
}): Promise<ApiResponse<IPRule>> {
  return post<IPRule>('/admin/security/ip-rules', params)
}

export function deleteIPRule(id: number): Promise<ApiResponse<void>> {
  return del<void>(`/admin/security/ip-rules/${id}`)
}

/* --- 风险标记 --- */

export interface RiskMark {
  id: number
  user_id: number
  username: string
  type: 'rpm_exceed' | 'anomaly' | 'manual'
  reason: string
  expires_at: string
  created_at: string
}

export function getRiskMarks(): Promise<ApiResponse<RiskMark[]>> {
  return get<RiskMark[]>('/admin/security/risk-marks')
}

export function createRiskMark(params: {
  user_id: number
  type: 'rpm_exceed' | 'anomaly' | 'manual'
  reason: string
  duration_hours: number
}): Promise<ApiResponse<RiskMark>> {
  return post<RiskMark>('/admin/security/risk-marks', params)
}

export function removeRiskMark(id: number): Promise<ApiResponse<void>> {
  return del<void>(`/admin/security/risk-marks/${id}`)
}

/* --- 异常事件 --- */

export type AnomalySeverity = 'low' | 'medium' | 'high' | 'critical'

export interface AnomalyEvent {
  id: number
  user_id: number
  username: string
  event_type: 'high_frequency' | 'model_scan' | 'error_spike'
  severity: AnomalySeverity
  detail: string
  created_at: string
}

export function getAnomalyEvents(
  limit?: number,
): Promise<ApiResponse<AnomalyEvent[]>> {
  const qs = limit ? `?limit=${limit}` : ''
  return get<AnomalyEvent[]>(`/admin/security/anomaly-events${qs}`)
}

export function getAuditLogs(filter: {
  page?: number
  page_size?: number
  action?: string
  start_date?: string
  end_date?: string
}): Promise<ApiResponse<{ items: AuditLogEntry[]; total: number }>> {
  const qs = new URLSearchParams()
  qs.set('page', String(filter.page ?? 1))
  qs.set('page_size', String(filter.page_size ?? 20))
  if (filter.action) qs.set('action', filter.action)
  if (filter.start_date) qs.set('start_date', filter.start_date)
  if (filter.end_date) qs.set('end_date', filter.end_date)
  return get<{ items: AuditLogEntry[]; total: number }>(`/admin/security/audit-logs?${qs.toString()}`)
}

/* ----------------------------------------------------------
 * 系统设置 API
 * ---------------------------------------------------------- */

export function getSMTPConfig(): Promise<ApiResponse<SMTPConfig>> {
  return get<SMTPConfig>('/admin/settings/smtp')
}

export function updateSMTPConfig(config: SMTPConfig): Promise<ApiResponse<void>> {
  return put<void>('/admin/settings/smtp', config)
}

export function testSMTPConnection(): Promise<ApiResponse<{ success: boolean; message: string }>> {
  return post<{ success: boolean; message: string }>('/admin/settings/smtp/test')
}

export function getOAuthProviders(): Promise<ApiResponse<OAuthProvider[]>> {
  return get<OAuthProvider[]>('/admin/settings/oauth-providers')
}

export function createOAuthProvider(params: {
  name: string
  provider: string
  client_id: string
  client_secret: string
}): Promise<ApiResponse<OAuthProvider>> {
  return post<OAuthProvider>('/admin/settings/oauth-providers', params)
}

export function deleteOAuthProvider(id: number): Promise<ApiResponse<void>> {
  return del<void>(`/admin/settings/oauth-providers/${id}`)
}

export function toggleOAuthProvider(
  id: number,
  enabled: boolean,
): Promise<ApiResponse<void>> {
  return put<void>(`/admin/settings/oauth-providers/${id}/toggle`, { enabled })
}

export interface GeneralSettings {
  jwt_secret_masked: string
  access_token_ttl: number
  refresh_token_ttl: number
  email_register_enabled: boolean
  invite_required: boolean
  referral_enabled: boolean
  daily_register_limit: number
}

export function getGeneralSettings(): Promise<ApiResponse<GeneralSettings>> {
  return get<GeneralSettings>('/admin/settings/general')
}

export function updateGeneralSettings(
  settings: Partial<GeneralSettings>,
): Promise<ApiResponse<void>> {
  return put<void>('/admin/settings/general', settings)
}

export function getPoolMode(): Promise<ApiResponse<{ mode: PoolMode }>> {
  return get<{ mode: PoolMode }>('/admin/settings/pool-mode')
}

export function updatePoolMode(mode: PoolMode): Promise<ApiResponse<void>> {
  return put<void>('/admin/settings/pool-mode', { mode })
}

/* ----------------------------------------------------------
 * 路由引擎 API
 * ---------------------------------------------------------- */

export function getRouterConfig(): Promise<ApiResponse<RouterConfig>> {
  return get<RouterConfig>('/admin/router/config')
}

export function updateRouterStrategy(strategy: RoutingStrategy): Promise<ApiResponse<void>> {
  return put<void>('/admin/router/strategy', { strategy })
}

export function updateCredentialWeight(
  credentialId: number,
  weight: number,
): Promise<ApiResponse<void>> {
  return put<void>(`/admin/router/credentials/${credentialId}/weight`, { weight })
}

export function updateHealthCheckerSettings(
  settings: HealthCheckerSettings,
): Promise<ApiResponse<void>> {
  return put<void>('/admin/router/health-settings', settings)
}

export function applyRouterChanges(): Promise<ApiResponse<void>> {
  return post<void>('/admin/router/apply')
}

/* ----------------------------------------------------------
 * 数据导出
 * ---------------------------------------------------------- */

export function exportStats(
  format: 'csv' | 'json',
): Promise<ApiResponse<ExportResponse>> {
  return get<ExportResponse>(`/admin/stats/export?format=${format}`)
}

/* ----------------------------------------------------------
 * 仪表盘 — 补充类型 & 接口
 * ---------------------------------------------------------- */

export interface DashboardStats {
  total_users: number
  total_requests_24h: number
  active_credentials: number
  system_health: 'healthy' | 'degraded' | 'down'
  users_trend: number
  requests_trend: number
  credentials_trend: number
}

export interface RequestTrend {
  date: string
  count: number
}

export interface ModelDistribution {
  model: string
  count: number
}

export interface AuditLog {
  id: number
  user_id: number | null
  action: string
  target: string
  detail: string
  ip: string
  created_at: string
}

export function fetchDashboardStats(): Promise<ApiResponse<DashboardStats>> {
  return get<DashboardStats>('/admin/dashboard/stats')
}

export function fetchRequestTrends(): Promise<ApiResponse<RequestTrend[]>> {
  return get<RequestTrend[]>('/admin/dashboard/request-trends')
}

export function fetchModelDistribution(): Promise<ApiResponse<ModelDistribution[]>> {
  return get<ModelDistribution[]>('/admin/dashboard/model-distribution')
}

export function fetchRecentAuditLogs(): Promise<ApiResponse<AuditLog[]>> {
  return get<AuditLog[]>('/admin/dashboard/recent-logs')
}

/* ----------------------------------------------------------
 * 用户管理 — 补充接口
 * ---------------------------------------------------------- */

export function fetchUsers(params: {
  page: number
  page_size: number
  search?: string
  role?: string
  status?: string
}): Promise<ApiResponse<PaginatedResponse<User>>> {
  const qs = new URLSearchParams()
  qs.set('page', String(params.page))
  qs.set('page_size', String(params.page_size))
  if (params.search) qs.set('search', params.search)
  if (params.role) qs.set('role', params.role)
  if (params.status) qs.set('status', params.status)
  return get<PaginatedResponse<User>>(`/admin/users?${qs.toString()}`)
}

export function updateUserStatus(
  userId: number,
  status: UserStatus,
): Promise<ApiResponse<void>> {
  return put<void>(`/admin/users/${userId}/status`, { status })
}

/* ----------------------------------------------------------
 * 额度 — RPM 设置
 * ---------------------------------------------------------- */

export interface RPMSettings {
  contributor_rpm: number
  non_contributor_rpm: number
}

export function fetchRPMSettings(): Promise<ApiResponse<RPMSettings>> {
  return get<RPMSettings>('/admin/settings/rpm')
}

export function updateRPMSettings(
  settings: RPMSettings,
): Promise<ApiResponse<void>> {
  return put<void>('/admin/settings/rpm', settings)
}

export function fetchQuotaConfigs(): Promise<ApiResponse<QuotaConfig[]>> {
  return get<QuotaConfig[]>('/admin/quota-configs')
}

/* ----------------------------------------------------------
 * 凭证池 — 补充接口
 * ---------------------------------------------------------- */

export function fetchCredentials(params?: {
  page?: number
  page_size?: number
  provider?: string
  health?: string
}): Promise<ApiResponse<PaginatedResponse<Credential>>> {
  const qs = new URLSearchParams()
  if (params?.page) qs.set('page', String(params.page))
  if (params?.page_size) qs.set('page_size', String(params.page_size))
  if (params?.provider) qs.set('provider', params.provider)
  if (params?.health) qs.set('health', params.health)
  return get<PaginatedResponse<Credential>>(`/admin/credentials?${qs.toString()}`)
}

export function createCredential(data: {
  provider: string
  credential_data: string
  pool_mode: PoolMode
}): Promise<ApiResponse<Credential>> {
  return post<Credential>('/admin/credentials', data)
}

/* ----------------------------------------------------------
 * 兑换码 — 补充类型 & 接口
 * ---------------------------------------------------------- */

export interface QuotaGrant {
  model_pattern: string
  requests: number
  tokens: number
  quota_type: QuotaType
}

export interface RedemptionTemplate {
  id: number
  name: string
  description: string
  bonus_quota: QuotaGrant
  max_per_user: number
  total_limit: number
  issued_count: number
  enabled: boolean
  created_at: string
}

export interface CodeUsageStat {
  template_id: number
  template_name: string
  total_generated: number
  total_used: number
  total_expired: number
}

export function fetchTemplates(): Promise<ApiResponse<RedemptionTemplate[]>> {
  return get<RedemptionTemplate[]>('/admin/redemption/templates')
}

export function createTemplate(
  tpl: Omit<RedemptionTemplate, 'id' | 'issued_count' | 'created_at'>,
): Promise<ApiResponse<RedemptionTemplate>> {
  return post<RedemptionTemplate>('/admin/redemption/templates', tpl)
}

export function updateTemplateEnabled(
  id: number,
  enabled: boolean,
): Promise<ApiResponse<void>> {
  return put<void>(`/admin/redemption/templates/${id}/toggle`, { enabled })
}

export function fetchCodeUsageStats(): Promise<ApiResponse<CodeUsageStat[]>> {
  return get<CodeUsageStat[]>('/admin/redemption/stats')
}

export function fetchRedemptionCodes(params: {
  page: number
  page_size: number
  template_id?: number
}): Promise<ApiResponse<PaginatedResponse<RedemptionCode>>> {
  const qs = new URLSearchParams()
  qs.set('page', String(params.page))
  qs.set('page_size', String(params.page_size))
  if (params.template_id) qs.set('template_id', String(params.template_id))
  return get<PaginatedResponse<RedemptionCode>>(
    `/admin/redemption/codes?${qs.toString()}`,
  )
}

