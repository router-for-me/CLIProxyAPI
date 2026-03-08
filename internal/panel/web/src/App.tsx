/* ============================================================
 * 应用根组件 — 路由配置
 *
 * 路由分层:
 * 1. 公开路由: /login, /register
 * 2. 用户路由: /dashboard, /quota, /credentials, /redeem, /settings
 * 3. 管理路由: /admin/*（需 admin 角色）
 *
 * 所有页面组件均使用 lazy() 懒加载以优化首屏体积。
 * ============================================================ */

import { Suspense, lazy } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from './stores/auth'
import MainLayout from './components/layout/MainLayout'

/* ----------------------------------------------------------
 * 懒加载页面组件
 * ---------------------------------------------------------- */

// 认证页
const Login = lazy(() => import('./pages/auth/Login'))
const Register = lazy(() => import('./pages/auth/Register'))

// 用户页
const Dashboard = lazy(() => import('./pages/user/Dashboard'))
const Quota = lazy(() => import('./pages/user/Quota'))
const Credentials = lazy(() => import('./pages/user/Credentials'))
const Redeem = lazy(() => import('./pages/user/Redeem'))
const Settings = lazy(() => import('./pages/user/Settings'))

// 管理页
const AdminDashboard = lazy(() => import('./pages/admin/Dashboard'))
const AdminUsers = lazy(() => import('./pages/admin/Users'))
const AdminQuota = lazy(() => import('./pages/admin/QuotaConfig'))
const AdminPool = lazy(() => import('./pages/admin/CredentialPool'))
const AdminCodes = lazy(() => import('./pages/admin/RedemptionCodes'))
const AdminInvites = lazy(() => import('./pages/admin/InviteCodes'))
const AdminSecurity = lazy(() => import('./pages/admin/Security'))
const AdminSettings = lazy(() => import('./pages/admin/SystemSettings'))
const AdminRouter = lazy(() => import('./pages/admin/RouterEngine'))

/* ----------------------------------------------------------
 * 路由守卫
 * ---------------------------------------------------------- */

/** 需要登录才能访问的路由 */
function RequireAuth({ children }: { children: React.ReactNode }) {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

/** 需要管理员权限的路由 */
function RequireAdmin({ children }: { children: React.ReactNode }) {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const isAdmin = useAuthStore((s) => s.isAdmin)

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }
  if (!isAdmin) {
    return <Navigate to="/dashboard" replace />
  }
  return <>{children}</>
}

/* ----------------------------------------------------------
 * 全局加载占位
 * ---------------------------------------------------------- */

function LoadingFallback() {
  return (
    <div className="flex items-center justify-center h-full min-h-[200px]">
      <div className="flex flex-col items-center gap-3">
        <div className="w-8 h-8 border-3 border-[#3B82F6] border-t-transparent rounded-full animate-spin" />
        <span className="text-sm text-[#64748B]">Loading...</span>
      </div>
    </div>
  )
}

/* ----------------------------------------------------------
 * App 主组件
 * ---------------------------------------------------------- */

export default function App() {
  return (
    <Suspense fallback={<LoadingFallback />}>
      <Routes>
        {/* ============ 公开路由 ============ */}
        <Route path="/login" element={<Login />} />
        <Route path="/register" element={<Register />} />

        {/* ============ 需要登录的路由（带布局） ============ */}
        <Route
          element={
            <RequireAuth>
              <MainLayout />
            </RequireAuth>
          }
        >
          {/* 用户端 */}
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/quota" element={<Quota />} />
          <Route path="/credentials" element={<Credentials />} />
          <Route path="/redeem" element={<Redeem />} />
          <Route path="/settings" element={<Settings />} />

          {/* 管理端 */}
          <Route
            path="/admin/dashboard"
            element={
              <RequireAdmin>
                <AdminDashboard />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/users"
            element={
              <RequireAdmin>
                <AdminUsers />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/quota"
            element={
              <RequireAdmin>
                <AdminQuota />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/pool"
            element={
              <RequireAdmin>
                <AdminPool />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/codes"
            element={
              <RequireAdmin>
                <AdminCodes />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/invites"
            element={
              <RequireAdmin>
                <AdminInvites />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/security"
            element={
              <RequireAdmin>
                <AdminSecurity />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/settings"
            element={
              <RequireAdmin>
                <AdminSettings />
              </RequireAdmin>
            }
          />
          <Route
            path="/admin/router"
            element={
              <RequireAdmin>
                <AdminRouter />
              </RequireAdmin>
            }
          />
        </Route>

        {/* ============ 根路径重定向 ============ */}
        <Route path="/" element={<Navigate to="/dashboard" replace />} />

        {/* ============ 404 回退 ============ */}
        <Route path="*" element={<Navigate to="/dashboard" replace />} />
      </Routes>
    </Suspense>
  )
}
