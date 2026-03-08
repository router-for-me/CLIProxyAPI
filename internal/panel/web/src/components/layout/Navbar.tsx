/* ============================================================
 * 顶部导航栏
 *
 * 功能:
 * 1. 移动端汉堡菜单按钮
 * 2. 显示当前页面标题（从 route location 推断）
 * 3. 用户头像 + 用户名
 * 4. 语言切换下拉框
 * ============================================================ */

import { useLocation } from 'react-router-dom'
import { useAuthStore } from '../../stores/auth'
import { useI18n, type Language } from '../../i18n'

/* ----------------------------------------------------------
 * 路径 -> 页面标题映射
 * ---------------------------------------------------------- */

const titleMap: Record<string, string> = {
  '/dashboard': 'nav.dashboard',
  '/quota': 'nav.quota',
  '/credentials': 'nav.credentials',
  '/redeem': 'nav.redeem',
  '/settings': 'nav.settings',
  '/admin/dashboard': 'nav.adminDashboard',
  '/admin/users': 'nav.adminUsers',
  '/admin/quota': 'nav.adminQuota',
  '/admin/pool': 'nav.adminPool',
  '/admin/codes': 'nav.adminCodes',
  '/admin/invites': 'nav.adminInvites',
  '/admin/security': 'nav.adminSecurity',
  '/admin/settings': 'nav.adminSettings',
  '/admin/router': 'nav.adminRouter',
}

interface NavbarProps {
  onToggleSidebar?: () => void
}

export default function Navbar({ onToggleSidebar }: NavbarProps) {
  const location = useLocation()
  const user = useAuthStore((s) => s.user)
  const { t, language, setLanguage } = useI18n()

  // 从路径推导页面标题
  const titleKey = titleMap[location.pathname] ?? 'nav.dashboard'
  const pageTitle = t(titleKey)

  // 用户名首字母作为头像
  const avatar = user?.username?.charAt(0).toUpperCase() ?? '?'

  return (
    <header className="h-16 bg-white border-b border-[#E2E8F0] flex items-center justify-between px-4 sm:px-6 shrink-0">
      {/* 左侧：汉堡菜单 + 页面标题 */}
      <div className="flex items-center gap-3">
        {/* 移动端汉堡菜单 */}
        {onToggleSidebar && (
          <button
            type="button"
            onClick={onToggleSidebar}
            className="lg:hidden p-1.5 -ml-1 rounded-lg text-[#64748B] hover:bg-[#F1F5F9] transition-colors"
            aria-label="Toggle sidebar"
          >
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <line x1="3" y1="6" x2="21" y2="6" />
              <line x1="3" y1="12" x2="21" y2="12" />
              <line x1="3" y1="18" x2="21" y2="18" />
            </svg>
          </button>
        )}
        <h1 className="text-lg font-semibold text-[#0F172A]">{pageTitle}</h1>
      </div>

      {/* 右侧：语言切换 + 用户信息 */}
      <div className="flex items-center gap-3 sm:gap-4">
        {/* 语言切换 */}
        <select
          value={language}
          onChange={(e) => setLanguage(e.target.value as Language)}
          className="h-8 px-2 text-sm text-[#64748B] bg-[#F8FAFC] border border-[#E2E8F0] rounded-lg cursor-pointer outline-none focus:border-[#3B82F6] transition-colors"
        >
          <option value="zh">{t('language.zh')}</option>
          <option value="en">{t('language.en')}</option>
        </select>

        {/* 用户头像 + 名称 */}
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-full bg-[#3B82F6] flex items-center justify-center text-white text-sm font-medium">
            {avatar}
          </div>
          <span className="text-sm font-medium text-[#0F172A] hidden sm:inline">
            {user?.username ?? '---'}
          </span>
        </div>
      </div>
    </header>
  )
}
