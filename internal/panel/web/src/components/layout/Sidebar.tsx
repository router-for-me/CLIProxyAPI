/* ============================================================
 * 侧边栏导航
 *
 * 功能:
 * 1. Logo / 品牌标识
 * 2. 用户端导航项（Dashboard / Quota / Credentials / Redeem / Settings）
 * 3. 管理端导航项（可折叠，仅 admin 角色可见）
 * 4. 退出登录按钮
 *
 * 设计:
 * - 固定 240px 宽，白色背景
 * - 当前路由项高亮蓝色
 * ============================================================ */

import { useState } from 'react'
import { NavLink, useNavigate } from 'react-router-dom'
import { useAuthStore } from '../../stores/auth'
import { useUserStore } from '../../stores/user'
import { useI18n } from '../../i18n'

/* ----------------------------------------------------------
 * SVG 图标 — 内联以避免外部依赖
 * ---------------------------------------------------------- */

function IconDashboard() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="3" width="7" height="7" rx="1" />
      <rect x="14" y="3" width="7" height="7" rx="1" />
      <rect x="3" y="14" width="7" height="7" rx="1" />
      <rect x="14" y="14" width="7" height="7" rx="1" />
    </svg>
  )
}

function IconQuota() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 2v20M2 12h20" />
      <circle cx="12" cy="12" r="10" />
    </svg>
  )
}

function IconCredentials() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="2" y="6" width="20" height="12" rx="2" />
      <path d="M12 12h.01" />
      <path d="M17 12h.01" />
      <path d="M7 12h.01" />
    </svg>
  )
}

function IconRedeem() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M20 12V8H6a2 2 0 0 1-2-2c0-1.1.9-2 2-2h12v4" />
      <path d="M4 6v12c0 1.1.9 2 2 2h14v-4" />
      <path d="M18 12a2 2 0 0 0 0 4h4v-4z" />
    </svg>
  )
}

function IconSettings() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  )
}

function IconAdmin() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 2L2 7l10 5 10-5-10-5z" />
      <path d="M2 17l10 5 10-5" />
      <path d="M2 12l10 5 10-5" />
    </svg>
  )
}

function IconUsers() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  )
}

function IconSecurity() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
    </svg>
  )
}

function IconRouter() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="2" y="14" width="20" height="6" rx="1" />
      <path d="M6 14V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v10" />
      <path d="M6 17h.01" />
      <path d="M10 17h.01" />
    </svg>
  )
}

function IconLogout() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <polyline points="16 17 21 12 16 7" />
      <line x1="21" y1="12" x2="9" y2="12" />
    </svg>
  )
}

function IconChevron({ open }: { open: boolean }) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`transition-transform duration-200 ${open ? 'rotate-90' : ''}`}
    >
      <polyline points="9 18 15 12 9 6" />
    </svg>
  )
}

/* ----------------------------------------------------------
 * NavItem 组件
 * ---------------------------------------------------------- */

interface NavItemProps {
  to: string
  icon: React.ReactNode
  label: string
}

function NavItem({ to, icon, label }: NavItemProps) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `flex items-center gap-3 px-4 py-2.5 rounded-xl text-sm font-medium transition-colors ${
          isActive
            ? 'bg-[#EFF6FF] text-[#3B82F6]'
            : 'text-[#64748B] hover:bg-[#F8FAFC] hover:text-[#0F172A]'
        }`
      }
    >
      {icon}
      <span>{label}</span>
    </NavLink>
  )
}

/* ----------------------------------------------------------
 * Sidebar 主组件
 * ---------------------------------------------------------- */

export default function Sidebar() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const isAdmin = useAuthStore((s) => s.isAdmin)
  const logout = useAuthStore((s) => s.logout)
  const resetUser = useUserStore((s) => s.reset)
  const [adminOpen, setAdminOpen] = useState(true)

  const handleLogout = () => {
    logout()
    resetUser()
    navigate('/login')
  }

  return (
    <aside className="w-[240px] h-screen bg-white border-r border-[#E2E8F0] flex flex-col fixed left-0 top-0 z-30">
      {/* ---- Logo ---- */}
      <div className="h-16 flex items-center px-5 border-b border-[#E2E8F0] shrink-0">
        <div className="flex items-center gap-2.5">
          <div className="w-8 h-8 rounded-lg bg-[#3B82F6] flex items-center justify-center">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="white" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 2L2 7l10 5 10-5-10-5z" />
              <path d="M2 17l10 5 10-5" />
              <path d="M2 12l10 5 10-5" />
            </svg>
          </div>
          <span className="text-base font-bold text-[#0F172A]">
            {t('common.appName')}
          </span>
        </div>
      </div>

      {/* ---- 导航区域（可滚动） ---- */}
      <nav className="flex-1 overflow-y-auto py-4 px-3 space-y-1">
        {/* 用户端导航 */}
        <NavItem to="/dashboard" icon={<IconDashboard />} label={t('nav.dashboard')} />
        <NavItem to="/quota" icon={<IconQuota />} label={t('nav.quota')} />
        <NavItem to="/credentials" icon={<IconCredentials />} label={t('nav.credentials')} />
        <NavItem to="/redeem" icon={<IconRedeem />} label={t('nav.redeem')} />
        <NavItem to="/settings" icon={<IconSettings />} label={t('nav.settings')} />

        {/* 管理端导航（仅 admin 可见） */}
        {isAdmin && (
          <>
            <div className="pt-4 pb-1">
              <button
                onClick={() => setAdminOpen(!adminOpen)}
                className="flex items-center justify-between w-full px-4 py-2 text-xs font-semibold text-[#94A3B8] uppercase tracking-wider hover:text-[#64748B] transition-colors"
              >
                <span>{t('nav.adminSection')}</span>
                <IconChevron open={adminOpen} />
              </button>
            </div>

            {adminOpen && (
              <div className="space-y-1">
                <NavItem to="/admin/dashboard" icon={<IconAdmin />} label={t('nav.adminDashboard')} />
                <NavItem to="/admin/users" icon={<IconUsers />} label={t('nav.adminUsers')} />
                <NavItem to="/admin/quota" icon={<IconQuota />} label={t('nav.adminQuota')} />
                <NavItem to="/admin/pool" icon={<IconCredentials />} label={t('nav.adminPool')} />
                <NavItem to="/admin/codes" icon={<IconRedeem />} label={t('nav.adminCodes')} />
                <NavItem to="/admin/invites" icon={<IconUsers />} label={t('nav.adminInvites')} />
                <NavItem to="/admin/security" icon={<IconSecurity />} label={t('nav.adminSecurity')} />
                <NavItem to="/admin/settings" icon={<IconSettings />} label={t('nav.adminSettings')} />
                <NavItem to="/admin/router" icon={<IconRouter />} label={t('nav.adminRouter')} />
              </div>
            )}
          </>
        )}
      </nav>

      {/* ---- 底部退出按钮 ---- */}
      <div className="p-3 border-t border-[#E2E8F0] shrink-0">
        <button
          onClick={handleLogout}
          className="flex items-center gap-3 w-full px-4 py-2.5 rounded-xl text-sm font-medium text-[#EF4444] hover:bg-[#FEF2F2] transition-colors"
        >
          <IconLogout />
          <span>{t('nav.logout')}</span>
        </button>
      </div>
    </aside>
  )
}
